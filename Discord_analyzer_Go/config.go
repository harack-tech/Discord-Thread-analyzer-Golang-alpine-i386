package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type config struct {
	DiscordToken        string
	GuildID             string
	DefaultHistoryLimit int
	LLMProvider         string
	GeminiAPIKey        string
	GeminiModel         string
	GeminiMaxTokens     int
	OpenAIAPIKey        string
	OpenAIModel         string
	OpenAIMaxTokens     int
}

func loadConfig() (config, error) {
	if err := loadDotEnv(".env"); err != nil && !os.IsNotExist(err) {
		return config{}, fmt.Errorf(".env の読み込みに失敗しました: %w", err)
	}

	defaultLimit, err := envInt("DEFAULT_HISTORY_LIMIT", 300)
	if err != nil {
		return config{}, err
	}
	geminiMaxTokens, err := envInt("GEMINI_MAX_OUTPUT_TOKENS", 4096)
	if err != nil {
		return config{}, err
	}
	openAIMaxTokens, err := envInt("OPENAI_MAX_OUTPUT_TOKENS", 4096)
	if err != nil {
		return config{}, err
	}
	cfg := config{
		DiscordToken:        strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN")),
		GuildID:             strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID")),
		DefaultHistoryLimit: defaultLimit,
		LLMProvider:         strings.ToLower(strings.TrimSpace(envString("LLM_PROVIDER", "gemini"))),
		GeminiAPIKey:        strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:         envString("GEMINI_MODEL", "gemini-flash-latest"),
		GeminiMaxTokens:     geminiMaxTokens,
		OpenAIAPIKey:        strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAIModel:         envString("OPENAI_MODEL", "gpt-5-mini"),
		OpenAIMaxTokens:     openAIMaxTokens,
	}

	if cfg.DiscordToken == "" {
		return config{}, fmt.Errorf("DISCORD_BOT_TOKEN が設定されていません。.env を確認してください")
	}
	if cfg.DefaultHistoryLimit < 1 || cfg.DefaultHistoryLimit > 1000 {
		return config{}, fmt.Errorf("DEFAULT_HISTORY_LIMIT は1〜1000の範囲で指定してください")
	}
	if cfg.GeminiMaxTokens < 1 || cfg.OpenAIMaxTokens < 1 {
		return config{}, fmt.Errorf("LLM_MAX_OUTPUT_TOKENS は1以上で指定してください")
	}
	switch cfg.LLMProvider {
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return config{}, fmt.Errorf("GEMINI_API_KEY が設定されていません。.env を確認してください")
		}
	case "openai":
		if cfg.OpenAIAPIKey == "" {
			return config{}, fmt.Errorf("OPENAI_API_KEY が設定されていません。.env を確認してください")
		}
	default:
		return config{}, fmt.Errorf("未対応の LLM_PROVIDER です: %q (gemini または openai を指定してください)", cfg.LLMProvider)
	}
	if cfg.GuildID != "" {
		if _, err := strconv.ParseUint(cfg.GuildID, 10, 64); err != nil {
			return config{}, fmt.Errorf("DISCORD_GUILD_ID が正しい数値IDではありません: %w", err)
		}
	}
	return cfg, nil
}

func envString(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s は整数で指定してください: %w", name, err)
	}
	return parsed, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		separator := strings.IndexByte(line, '=')
		if separator < 0 || strings.TrimSpace(line[:separator]) == "" {
			return fmt.Errorf("%s:%d: KEY=VALUE の形式ではありません", path, lineNumber)
		}
		name, value := line[:separator], line[separator+1:]
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(name); !exists {
			if err := os.Setenv(name, value); err != nil {
				return fmt.Errorf("%s:%d: 環境変数 %s を設定できません: %w", path, lineNumber, name, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s の読み込み中にエラーが発生しました: %w", path, err)
	}
	return nil
}
