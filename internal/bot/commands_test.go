package bot

import (
	"context"
	"testing"

	"github.com/gotd/td/telegram/message/inline"
	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/go-faster/sisyphus/internal/agent"
)

func TestCommandRegistryHelpText(t *testing.T) {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})
	reg := b.buildCommandRegistry(context.Background())

	want := "Available commands:\n" +
		"/help \u2014 Show this message\n" +
		"/context <question> \u2014 search indexed knowledge and answer a question\n" +
		"/search <query> \u2014 raw ranked search results, no summary\n" +
		"/investigate <description> \u2014 run an on-demand investigation"
	require.Equal(t, want, reg.helpText())
}

func TestCommandRegistryHelpTextHidesHiddenAndEmptyDesc(t *testing.T) {
	reg := newCommandRegistry()
	reg.add("visible", "", "a visible command", false, func(context.Context, messageSender, int64, string) error { return nil })
	reg.add("hidden", "", "should not appear", true, func(context.Context, messageSender, int64, string) error { return nil })
	reg.add("nodesc", "<arg>", "", false, func(context.Context, messageSender, int64, string) error { return nil })

	text := reg.helpText()
	require.Contains(t, text, "/visible \u2014 a visible command")
	require.NotContains(t, text, "hidden")
	require.NotContains(t, text, "nodesc")
}

func TestCommandRegistryLookup(t *testing.T) {
	reg := newCommandRegistry()
	called := 0
	reg.add("ping", "", "ping", false, func(context.Context, messageSender, int64, string) error {
		called++
		return nil
	})

	c, ok := reg.lookup("ping")
	require.True(t, ok)
	require.Equal(t, "ping", c.name)

	_, ok = reg.lookup("nonexistent")
	require.False(t, ok)

	require.NoError(t, c.handler(context.Background(), silentSender{}, 1, ""))
	require.Equal(t, 1, called)
}

func TestCommandRegistryBotCommands(t *testing.T) {
	reg := newCommandRegistry()
	reg.add("a", "<x>", "alpha", false, func(context.Context, messageSender, int64, string) error { return nil })
	reg.add("b", "", "beta", false, func(context.Context, messageSender, int64, string) error { return nil })
	reg.add("hidden", "", "secret", true, func(context.Context, messageSender, int64, string) error { return nil })
	reg.add("nodesc", "<y>", "", false, func(context.Context, messageSender, int64, string) error { return nil })

	cmds := reg.botCommands()
	require.Len(t, cmds, 2)
	require.Equal(t, "a", cmds[0].Command)
	require.Equal(t, "alpha", cmds[0].Description)
	require.Equal(t, "b", cmds[1].Command)
}

func TestBuildCommandRegistryOrder(t *testing.T) {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
	})
	reg := b.buildCommandRegistry(context.Background())

	expected := []string{"start", "help", "context", "search", "investigate"}
	require.Len(t, reg.cmds, len(expected))
	for i, name := range expected {
		require.Equal(t, name, reg.cmds[i].name, "command %d", i)
	}
}

func TestBuildCommandRegistryStartIsHidden(t *testing.T) {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         true,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
	})
	reg := b.buildCommandRegistry(context.Background())

	c, ok := reg.lookup("start")
	require.True(t, ok)
	require.True(t, c.hidden, "/start should be hidden from /help and /-menu")
}

func TestHandleStartCmdIncludesUserIDAndHelp(t *testing.T) {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         false,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})
	b.commands = b.buildCommandRegistry(context.Background())
	c, _ := b.commands.lookup("start")

	var sent string
	stub := &fakeSender{onSendText: func(_ context.Context, text string) (int, error) {
		sent = text
		return 0, nil
	}}

	require.NoError(t, c.handler(context.Background(), stub, 42, ""))
	require.Contains(t, sent, "Your ID: 42")
	require.Contains(t, sent, "/context")
	require.Contains(t, sent, "/help")
}

func TestHandleHelpCmdSendsGeneratedHelp(t *testing.T) {
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         false,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
	})
	b.commands = b.buildCommandRegistry(context.Background())
	c, _ := b.commands.lookup("help")

	var sent string
	stub := &fakeSender{onSendText: func(_ context.Context, text string) (int, error) {
		sent = text
		return 0, nil
	}}

	require.NoError(t, c.handler(context.Background(), stub, 0, ""))
	require.Equal(t, b.commands.helpText(), sent)
}

func TestHandleInvestigateCmdAcksAndOffloads(t *testing.T) {
	inv := &fakeInvestigator{report: agent.Report{Verdict: agent.VerdictSolved}}
	b := New(context.Background(), nil, nil, BotCredentials{}, BotOptions{
		Silent:         false,
		TracerProvider: otel.GetTracerProvider(),
		Logger:         zap.NewNop(),
		AllowedUserIDs: []int64{1},
		Investigator:   inv,
	})
	b.commands = b.buildCommandRegistry(context.Background())
	c, _ := b.commands.lookup("investigate")

	var sent []string
	stub := &fakeSender{
		onSendText: func(_ context.Context, text string) (int, error) {
			sent = append(sent, text)
			return 0, nil
		},
		onSendStyled: func(_ context.Context, _ string, _ tg.ReplyMarkupClass) error { return nil },
	}

	require.NoError(t, c.handler(context.Background(), stub, 0, "something bad"))
	require.Len(t, sent, 1)
	require.Contains(t, sent[0], "Investigating, this may take a few minutes")
}

type fakeInvestigator struct {
	report agent.Report
	err    error
	called bool
}

func (f *fakeInvestigator) Investigate(_ context.Context, _ string) (agent.Report, error) {
	f.called = true
	return f.report, f.err
}

type fakeSender struct {
	onSendText   func(ctx context.Context, text string) (int, error)
	onEditStyled func(ctx context.Context, msgID int, md string, kb tg.ReplyMarkupClass) error
	onSendStyled func(ctx context.Context, md string, kb tg.ReplyMarkupClass) error
}

func (f *fakeSender) sendText(ctx context.Context, text string) (int, error) {
	if f.onSendText != nil {
		return f.onSendText(ctx, text)
	}
	return 0, nil
}

func (f *fakeSender) editStyled(ctx context.Context, msgID int, md string, kb tg.ReplyMarkupClass) error {
	if f.onEditStyled != nil {
		return f.onEditStyled(ctx, msgID, md, kb)
	}
	return nil
}

func (f *fakeSender) sendStyled(ctx context.Context, md string, kb tg.ReplyMarkupClass) error {
	if f.onSendStyled != nil {
		return f.onSendStyled(ctx, md, kb)
	}
	return nil
}

func (f *fakeSender) setInline(_ context.Context, _ ...inline.ResultOption) (bool, error) {
	return false, nil
}

var _ messageSender = (*fakeSender)(nil)
