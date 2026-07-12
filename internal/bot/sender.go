package bot

import (
	"context"

	"github.com/go-faster/errors"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/inline"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
)

type messageSender interface {
	sendText(ctx context.Context, text string) (int, error)
	editStyled(ctx context.Context, msgID int, md string, kb tg.ReplyMarkupClass) error
	sendStyled(ctx context.Context, md string, kb tg.ReplyMarkupClass) error
	setInline(ctx context.Context, opts ...inline.ResultOption) (bool, error)
}

type gotdSender struct {
	sender   *message.Sender
	entities tg.Entities
	msg      *tg.UpdateNewMessage

	inline *inline.ResultBuilder
}

func newReplySender(s *message.Sender, e tg.Entities, m *tg.UpdateNewMessage) *gotdSender {
	return &gotdSender{sender: s, entities: e, msg: m}
}

func newInlineSender(b *inline.ResultBuilder) *gotdSender {
	return &gotdSender{inline: b}
}

func (g *gotdSender) sendText(ctx context.Context, text string) (int, error) {
	if g.inline != nil {
		return 0, errors.New("sendText not supported for inline")
	}
	return unpack.MessageID(g.sender.Reply(g.entities, g.msg).Text(ctx, text))
}

func (g *gotdSender) editStyled(ctx context.Context, msgID int, md string, kb tg.ReplyMarkupClass) error {
	if g.inline != nil {
		return errors.New("editStyled not supported for inline")
	}
	b := g.sender.Answer(g.entities, g.msg)
	edit := b.Edit(msgID)
	if kb != nil {
		edit = b.Markup(kb).Edit(msgID)
	}
	_, err := edit.StyledText(ctx, styling.Custom(func(eb *entity.Builder) error {
		return renderMarkdown(eb, md)
	}))
	if err == nil {
		return nil
	}
	if tg.IsMessageNotModified(err) {
		return err
	}
	_, err = b.Edit(msgID).Text(ctx, md)
	return err
}

func (g *gotdSender) sendStyled(ctx context.Context, md string, kb tg.ReplyMarkupClass) error {
	if g.inline != nil {
		return errors.New("sendStyled not supported for inline")
	}
	req := g.sender.Reply(g.entities, g.msg)
	if kb != nil {
		req = req.Markup(kb)
	}
	_, err := req.StyledText(ctx, styling.Custom(func(eb *entity.Builder) error {
		return renderMarkdown(eb, md)
	}))
	if err == nil {
		return nil
	}
	_, err = req.Text(ctx, md)
	return err
}

func (g *gotdSender) setInline(ctx context.Context, opts ...inline.ResultOption) (bool, error) {
	if g.inline == nil {
		return false, errors.New("setInline requires inline builder")
	}
	return g.inline.Set(ctx, opts...)
}

type silentSender struct{}

func (silentSender) sendText(_ context.Context, _ string) (int, error) { return 0, nil }

func (silentSender) editStyled(_ context.Context, _ int, _ string, _ tg.ReplyMarkupClass) error {
	return nil
}

func (silentSender) sendStyled(_ context.Context, _ string, _ tg.ReplyMarkupClass) error { return nil }

func (silentSender) setInline(_ context.Context, _ ...inline.ResultOption) (bool, error) {
	return true, nil
}
