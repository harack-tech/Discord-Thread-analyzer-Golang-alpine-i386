package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

const (
	messageChunkSize = 1900
	workerCount      = 4
	queueCapacity    = 32
	pingResponseText = "Pong! Botは正常に応答しています。"
)

type queuedInteraction struct {
	interaction *discordgo.InteractionCreate
	mode        string
	limit       int
}

type workerPool struct {
	session *discordgo.Session
	llm     *llmClient
	jobs    chan queuedInteraction
	mu      sync.Mutex
	open    bool
	wg      sync.WaitGroup
}

func newWorkerPool(session *discordgo.Session, llm *llmClient) *workerPool {
	pool := &workerPool{
		session: session,
		llm:     llm,
		jobs:    make(chan queuedInteraction, queueCapacity),
		open:    true,
	}
	for i := 0; i < workerCount; i++ {
		pool.wg.Add(1)
		go pool.work()
	}
	return pool
}

func (p *workerPool) enqueue(job queuedInteraction) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.open {
		return false
	}
	select {
	case p.jobs <- job:
		return true
	default:
		return false
	}
}

func (p *workerPool) stop() {
	p.mu.Lock()
	if p.open {
		p.open = false
		close(p.jobs)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *workerPool) work() {
	defer p.wg.Done()
	for job := range p.jobs {
		p.runSafely(job)
	}
}

func (p *workerPool) runSafely(job queuedInteraction) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Printf("interaction 処理中の panic を回復しました: %v\n%s", recovered, debug.Stack())
			p.sendFollowup(job.interaction, "内部エラーが発生しました。時間をおいて再度お試しください。")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := p.handle(ctx, job); err != nil {
		logger.Printf("interaction の処理に失敗しました: %v", err)
	}
}

func (p *workerPool) handle(ctx context.Context, job queuedInteraction) error {
	interaction := job.interaction
	var channel *discordgo.Channel
	err := retry(ctx, "チャンネル情報の取得", retryableDiscordError, func() error {
		var callErr error
		channel, callErr = p.session.Channel(interaction.ChannelID)
		return callErr
	})
	if err != nil {
		p.sendFollowup(interaction, "スレッド情報の取得中にエラーが発生しました。時間をおいて再度お試しください。")
		return fmt.Errorf("チャンネル情報の取得に失敗しました: %w", err)
	}
	if !isThread(channel.Type) {
		p.sendFollowup(interaction, "このコマンドはスレッド内でのみ実行できます。")
		return nil
	}

	messages, err := fetchThreadMessages(ctx, p.session, channel.ID, job.limit)
	if err != nil {
		p.sendFollowup(interaction, "スレッド履歴の取得中にエラーが発生しました。時間をおいて再度お試しください。")
		return fmt.Errorf("スレッド履歴の取得に失敗しました: %w", err)
	}
	logText := formatThreadLog(messages)
	if logText == "" {
		p.sendFollowup(interaction, "解析対象となる発言が見つかりませんでした。")
		return nil
	}
	result, err := p.llm.analyze(ctx, job.mode, logText)
	if err != nil {
		p.sendFollowup(interaction, "解析中にエラーが発生しました。しばらくしてから再度お試しください。")
		return fmt.Errorf("LLM解析に失敗しました: %w", err)
	}
	if result == "" {
		p.sendFollowup(interaction, "解析結果が空でした。もう一度お試しください。")
		return nil
	}

	chunks := splitForDiscord(result, messageChunkSize)
	if err := p.followupWithRetry(ctx, interaction, chunks[0]); err != nil {
		return fmt.Errorf("解析結果の送信に失敗しました: %w", err)
	}
	for _, chunk := range chunks[1:] {
		if err := p.sendChannelMessage(ctx, channel.ID, chunk); err != nil {
			return fmt.Errorf("解析結果の続きの送信に失敗しました: %w", err)
		}
	}
	return nil
}

func (p *workerPool) sendFollowup(interaction *discordgo.InteractionCreate, content string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := retry(ctx, "Discord followup", retryableDiscordError, func() error {
		_, callErr := p.session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{Content: content})
		return callErr
	})
	if err != nil {
		logger.Printf("Discord へのエラー通知に失敗しました: %v", err)
	}
}

func (p *workerPool) followupWithRetry(ctx context.Context, interaction *discordgo.InteractionCreate, content string) error {
	return retry(ctx, "解析結果の返信", retryableDiscordError, func() error {
		_, err := p.session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{Content: content})
		return err
	})
}

func (p *workerPool) sendChannelMessage(ctx context.Context, channelID, content string) error {
	return retry(ctx, "スレッドへの返信", retryableDiscordError, func() error {
		_, err := p.session.ChannelMessageSend(channelID, content)
		return err
	})
}

func isThread(channelType discordgo.ChannelType) bool {
	return channelType == discordgo.ChannelTypeGuildPublicThread ||
		channelType == discordgo.ChannelTypeGuildPrivateThread ||
		channelType == discordgo.ChannelTypeGuildNewsThread
}

func fetchThreadMessages(ctx context.Context, session *discordgo.Session, channelID string, limit int) ([]*discordgo.Message, error) {
	messages := make([]*discordgo.Message, 0, limit)
	beforeID := ""
	for len(messages) < limit {
		pageSize := limit - len(messages)
		if pageSize > 100 {
			pageSize = 100
		}
		var page []*discordgo.Message
		err := retry(ctx, "スレッド履歴の取得", retryableDiscordError, func() error {
			var callErr error
			page, callErr = session.ChannelMessages(channelID, pageSize, beforeID, "", "")
			return callErr
		})
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		messages = append(messages, page...)
		beforeID = page[len(page)-1].ID
		if len(page) < pageSize {
			break
		}
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages, nil
}

func formatThreadLog(messages []*discordgo.Message) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Author == nil || message.Author.Bot {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" && len(message.Attachments) == 0 {
			continue
		}
		name := message.Author.GlobalName
		if message.Member != nil && message.Member.Nick != "" {
			name = message.Member.Nick
		}
		if name == "" {
			name = message.Author.Username
		}
		line := fmt.Sprintf("[%s] %s: %s", message.Timestamp.UTC().Format("2006-01-02 15:04"), name, content)
		if len(message.Attachments) > 0 {
			names := make([]string, 0, len(message.Attachments))
			for _, attachment := range message.Attachments {
				names = append(names, attachment.Filename)
			}
			line += " (添付ファイル: " + strings.Join(names, ", ") + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func splitForDiscord(text string, chunkSize int) []string {
	if utf8.RuneCountInString(text) <= chunkSize {
		return []string{text}
	}
	chunks := make([]string, 0, utf8.RuneCountInString(text)/chunkSize+1)
	current := make([]rune, 0, chunkSize)
	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, string(current))
			current = current[:0]
		}
	}
	for _, line := range strings.Split(text, "\n") {
		lineRunes := []rune(line)
		for len(lineRunes) > chunkSize {
			flush()
			chunks = append(chunks, string(lineRunes[:chunkSize]))
			lineRunes = lineRunes[chunkSize:]
		}
		addedLength := len(lineRunes)
		if len(current) > 0 {
			addedLength++
		}
		if len(current)+addedLength > chunkSize {
			flush()
		}
		if len(current) > 0 {
			current = append(current, '\n')
		}
		current = append(current, lineRunes...)
	}
	flush()
	return chunks
}

func applicationCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
			Name:        "ping",
			Description: "Botの応答を確認します。",
		},
		{
			Name:        "analyze",
			Description: "このスレッドのチャットログを指定モードで解析します。",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "mode",
					Description: "実行モードを選択してください。",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "要約 (summarize)", Value: "summarize"},
						{Name: "認識ズレ指摘 (gap_analysis)", Value: "gap_analysis"},
						{Name: "簡易マネジメント (task_management)", Value: "task_management"},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "limit",
					Description: "解析対象とする直近メッセージ件数（デフォルト: 設定値、最大1000）",
					Required:    false,
				},
			},
		},
	}
}

func registerCommands(session *discordgo.Session, appID, guildID string) error {
	for _, command := range applicationCommands() {
		if _, err := session.ApplicationCommandCreate(appID, guildID, command); err != nil {
			return fmt.Errorf("/%s の登録に失敗しました: %w", command.Name, err)
		}
	}
	return nil
}

func handleInteraction(session *discordgo.Session, pool *workerPool, defaultLimit int, interaction *discordgo.InteractionCreate) {
	if interaction.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := interaction.ApplicationCommandData()
	logger.Printf("スラッシュコマンドを受信しました: /%s", data.Name)
	if data.Name == "ping" {
		respond(session, interaction, pingResponseText)
		return
	}
	if data.Name != "analyze" {
		return
	}
	mode := ""
	limit := defaultLimit
	for _, option := range data.Options {
		switch option.Name {
		case "mode":
			mode = option.StringValue()
		case "limit":
			limit = int(option.IntValue())
		}
	}
	if mode != "summarize" && mode != "gap_analysis" && mode != "task_management" {
		respondEphemeral(session, interaction, "mode を有効な実行モードから選択してください。")
		return
	}
	if limit < 1 || limit > 1000 {
		respondEphemeral(session, interaction, "limit は 1〜1000 の範囲で指定してください。")
		return
	}
	if err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		logger.Printf("interaction の defer に失敗しました: %v", err)
		return
	}
	if !pool.enqueue(queuedInteraction{interaction: interaction, mode: mode, limit: limit}) {
		pool.sendFollowup(interaction, "現在処理が混み合っています。しばらくしてから再度お試しください。")
	}
}

func respondEphemeral(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) {
	respondWithFlags(session, interaction, content, discordgo.MessageFlagsEphemeral)
}

func respond(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) {
	respondWithFlags(session, interaction, content, 0)
}

func respondWithFlags(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string, flags discordgo.MessageFlags) {
	if err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   flags,
		},
	}); err != nil {
		logger.Printf("interaction への応答に失敗しました: %v", err)
	}
}

func recoverHandlerPanic(name string) {
	if recovered := recover(); recovered != nil {
		logger.Printf("%s 内の panic を回復しました: %v\n%s", name, recovered, debug.Stack())
	}
}
