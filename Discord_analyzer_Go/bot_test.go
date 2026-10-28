package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestFormatThreadLog(t *testing.T) {
	messages := []*discordgo.Message{
		{
			Author:    &discordgo.User{Username: "bot", Bot: true},
			Content:   "解析結果",
			Timestamp: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
		},
		{
			Author:    &discordgo.User{Username: "member", GlobalName: "表示名"},
			Content:   " 相談します ",
			Timestamp: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
			Attachments: []*discordgo.MessageAttachment{
				{Filename: "design.png"},
				{Filename: "log.txt"},
			},
		},
		{
			Author:    &discordgo.User{Username: "empty"},
			Timestamp: time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC),
		},
	}

	got := formatThreadLog(messages)
	want := "[2026-01-02 03:04] 表示名: 相談します (添付ファイル: design.png, log.txt)"
	if got != want {
		t.Fatalf("formatThreadLog() = %q, want %q", got, want)
	}
}

func TestSplitForDiscordUsesRunesAndPreservesText(t *testing.T) {
	input := strings.Repeat("あ", 5) + "\n" + strings.Repeat("い", 5)
	chunks := splitForDiscord(input, 6)

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %#v", len(chunks), chunks)
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 6 {
			t.Fatalf("chunk has %d runes, maximum is 6", len([]rune(chunk)))
		}
	}
	if got := strings.Join(chunks, "\n"); got != input {
		t.Fatalf("joined chunks = %q, want %q", got, input)
	}
}

func TestSplitForDiscordSplitsLongLines(t *testing.T) {
	chunks := splitForDiscord("abcdefgh", 3)
	want := []string{"abc", "def", "gh"}
	if len(chunks) != len(want) {
		t.Fatalf("got %#v, want %#v", chunks, want)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestBuildUserMessage(t *testing.T) {
	got := buildUserMessage("summarize", "sample log")
	want := "[実行モード]\nmode: summarize\n\n[チャットログ]\nsample log"
	if got != want {
		t.Fatalf("buildUserMessage() = %q, want %q", got, want)
	}
}

func TestApplicationCommandsIncludePingAndAnalyze(t *testing.T) {
	commands := applicationCommands()
	names := make(map[string]bool, len(commands))
	for _, command := range commands {
		names[command.Name] = true
	}
	if !names["ping"] || !names["analyze"] || len(commands) != 2 {
		t.Fatalf("registered command names = %#v, want /ping and /analyze", names)
	}
	if pingResponseText == "" {
		t.Fatal("/ping must return a non-empty interaction response")
	}
}
