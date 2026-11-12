	rdb := testutil.NewRedis(t)
	return &MessagePublisher{rdb: rdb}, rdb
}

// TestMessagePublisher_PublishesToConversationChannel 广播要发到
// `chatroom:msg:{conversationId}` 频道上。
func TestMessagePublisher_PublishesToConversationChannel(t *testing.T) {
	p, rdb := newTestMessagePublisher(t)
	ctx := context.Background()

	const convID = 900101
	channel := messageChannel(convID)

	sub := rdb.Subscribe(ctx, channel)
	t.Cleanup(func() { _ = sub.Close() })
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	msg := &biz.Message{
		ID:             55,
		ConversationID: convID,
		SenderID:       "uid-42",
		Type:           biz.MessageTypeText,
		Content:        "hello",
		CreatedAt:      time.Unix(1700000000, 0),
	}
	if err := p.Publish(ctx, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ch := sub.Channel()
	select {
	case got := <-ch:
		var payload PublishPayload
		if err := json.Unmarshal([]byte(got.Payload), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if payload.MessageID != 55 {
			t.Errorf("message id = %d, want 55", payload.MessageID)
		}
		if payload.ConversationID != convID {
			t.Errorf("conversation id = %d, want %d", payload.ConversationID, convID)
		}
		if payload.SenderID != "uid-42" {
			t.Errorf("sender id = %q, want uid-42", payload.SenderID)
		}
		if payload.Content != "hello" {
			t.Errorf("content = %q, want %q", payload.Content, "hello")
		}
		if payload.CreatedAt != 1700000000 {
			t.Errorf("created at = %d, want 1700000000", payload.CreatedAt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("没收到广播")
	}
}

// TestMessagePublisher_NilMessageReturnsError nil 入参返参数错误。
func TestMessagePublisher_NilMessageReturnsError(t *testing.T) {
	p, _ := newTestMessagePublisher(t)

	if err := p.Publish(context.Background(), nil); err == nil {
		t.Fatal("nil 入参该返错误")
	}
}

// TestMessageChannel_Format 频道名格式固定。
func TestMessageChannel_Format(t *testing.T) {
	if got := messageChannel(7); got != "chatroom:msg:7" {
		t.Errorf("channel = %q, want %q", got, "chatroom:msg:7")
	}
}
