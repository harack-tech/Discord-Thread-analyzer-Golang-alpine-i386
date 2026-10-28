package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

var logger = log.New(os.Stderr, "discord-analyzer: ", log.LstdFlags|log.LUTC)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	retryDelay := time.Second
	for {
		err := run(ctx)
		if err == nil {
			return
		}
		var panicErr *recoveredPanicError
		if !errors.As(err, &panicErr) {
			logger.Printf("起動に失敗しました: %v", err)
			os.Exit(1)
		}
		if ctx.Err() != nil {
			return
		}
		logger.Printf("panic から復旧するため Bot を再起動します (%s後)", retryDelay)
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		retryDelay *= 2
		if retryDelay > 30*time.Second {
			retryDelay = 30 * time.Second
		}
	}
}

type recoveredPanicError struct {
	value interface{}
}

func (e *recoveredPanicError) Error() string {
	return fmt.Sprintf("Bot main routine panic: %v", e.value)
}

func run(ctx context.Context) (runErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = &recoveredPanicError{value: recovered}
			logger.Printf("main routine panic を回復しました: %v\n%s", recovered, debug.Stack())
		}
	}()
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	syncCtx, cancelSync := context.WithCancel(ctx)

	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		cancelSync()
		return fmt.Errorf("Discord セッションの作成に失敗しました: %w", err)
	}
	session.Client = &http.Client{Timeout: 30 * time.Second}
	session.ShouldReconnectOnError = true
	session.StateEnabled = false
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	pool := newWorkerPool(session, newLLMClient(cfg))
	commandSync := make(chan string, 1)
	var syncWG sync.WaitGroup
	syncWG.Add(1)
	go func() {
		defer syncWG.Done()
		for {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						logger.Printf("コマンド同期ワーカーの panic を回復しました: %v\n%s", recovered, debug.Stack())
					}
				}()
				syncCommands(syncCtx, session, cfg.GuildID, commandSync)
			}()
			if syncCtx.Err() != nil {
				return
			}
			logger.Printf("コマンド同期ワーカーを再起動します")
		}
	}()
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			cancelSync()
			if err := session.Close(); err != nil {
				logger.Printf("Discord セッションの終了中にエラーが発生しました: %v", err)
			}
			pool.stop()
			syncWG.Wait()
		})
	}
	defer shutdown()
	session.AddHandler(func(_ *discordgo.Session, ready *discordgo.Ready) {
		defer recoverHandlerPanic("Ready handler")
		if ready.User == nil {
			logger.Printf("Ready イベントに bot user が含まれていません")
			return
		}
		select {
		case commandSync <- ready.User.ID:
		default:
			logger.Printf("コマンド同期がキューにあるため Ready イベントを保留しました")
		}
	})
	session.AddHandler(func(s *discordgo.Session, interaction *discordgo.InteractionCreate) {
		defer recoverHandlerPanic("interaction handler")
		handleInteraction(s, pool, cfg.DefaultHistoryLimit, interaction)
	})

	if err := retry(ctx, "Discord Gateway 接続", retryableDiscordError, session.Open); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("Discord Gateway に接続できません: %w", err)
	}
	logger.Printf("Discord に接続しました。SIGINT/SIGTERM で安全に終了します")
	<-ctx.Done()
	logger.Printf("終了要求を受信しました。処理中のタスクを完了して終了します")

	shutdown()
	logger.Printf("すべての処理が終了しました")
	return nil
}

func syncCommands(ctx context.Context, session *discordgo.Session, guildID string, requests <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case appID := <-requests:
			err := retry(ctx, "スラッシュコマンド同期", retryableDiscordError, func() error {
				return registerCommands(session, appID, guildID)
			})
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					logger.Printf("スラッシュコマンドの同期に失敗しました: %v", err)
				}
				continue
			}
			scope := "global"
			if guildID != "" {
				scope = "guild:" + guildID
			}
			logger.Printf("スラッシュコマンドを同期しました (/ping, /analyze; scope=%s)", scope)
		}
	}
}
