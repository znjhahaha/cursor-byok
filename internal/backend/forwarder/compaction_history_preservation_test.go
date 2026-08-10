package forwarder

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func TestPersistCompactionStateKeepsVisibleHistoryAndCompactsPromptReplay(t *testing.T) {
	const (
		conversationID = "conversation-compaction-history"
		oldRequestID   = "request-old"
		newRequestID   = "request-current"
	)
	store := NewConversationFileStore(t.TempDir())
	if _, err := store.CreateConversation(
		conversationID,
		agentv1.AgentMode_AGENT_MODE_AGENT,
		"",
		"",
		conversationID,
	); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	originalEntries := []HistoryEntry{
		compactionHistoryUserEntry(t, 1, oldRequestID, "message-old", "old question"),
		newAssistantTextEntry(1, oldRequestID, "old answer", "", ""),
		compactionHistoryUserEntry(t, 2, newRequestID, "message-current", "current question"),
	}
	conversation, _, err := store.AppendEntries(conversationID, originalEntries)
	if err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	conversation, err = store.UpdateConversationMeta(conversationID, func(item *ConversationFile) error {
		item.TokenDetailsUsedTokens = 120000
		item.TokenDetailsMaxTokens = 130000
		item.AutoCompactionPending = true
		item.AutoCompactionPromptTokens = 120000
		item.AutoCompactionReserveTokens = 10000
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateConversationMeta() error = %v", err)
	}

	plan := &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          newRequestID,
		CurrentUserText:           "current question",
		PreserveCurrentTurnInputs: true,
		CompactTurnCount:          2,
		MessagesToCompact:         3,
	}
	compactionEntries, err := buildCompactionStateEntries(conversation, plan, "stable summary")
	if err != nil {
		t.Fatalf("buildCompactionStateEntries() error = %v", err)
	}
	compacted := cloneConversationFile(conversation)
	if err := applyCompactionToConversation(compacted, plan, "stable summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}

	broker := NewStreamBroker()
	stream, err := broker.OpenStream(
		newRequestID,
		conversationID,
		2,
		"default",
		"default",
		agentv1.AgentMode_AGENT_MODE_AGENT,
		"current question",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := (&Service{store: store}).persistCompactionState(stream, conversationID, compacted, compactionEntries); err != nil {
		t.Fatalf("persistCompactionState() error = %v", err)
	}

	loaded, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if got, want := len(loaded.Entries), len(originalEntries)+len(compactionEntries); got != want {
		t.Fatalf("persisted entries = %d, want %d", got, want)
	}
	if got := loaded.Entries[0].Kind; got != "user_message" {
		t.Fatalf("first persisted kind = %q, want original user_message", got)
	}
	if got := loaded.Entries[1].Kind; got != "assistant_text" {
		t.Fatalf("second persisted kind = %q, want original assistant_text", got)
	}
	if got := loaded.Entries[len(originalEntries)].Kind; got != "compacted_summary" {
		t.Fatalf("first appended compaction kind = %q, want compacted_summary", got)
	}
	if loaded.TokenDetailsUsedTokens != 0 || loaded.AutoCompactionPending {
		t.Fatalf("compaction metadata not cleared: used=%d pending=%v", loaded.TokenDetailsUsedTokens, loaded.AutoCompactionPending)
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(loaded)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if got := len(projection.State.GetTurns()); got != 2 {
		t.Fatalf("checkpoint turns = %d, want both visible turns", got)
	}

	replay, err := NewHistoryProjector().ProjectPromptReplay(loaded)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	if got := len(replay); got != 2 {
		t.Fatalf("prompt replay messages = %d, want summary plus current user", got)
	}
	if !strings.Contains(replay[0].Content, "stable summary") {
		t.Fatalf("first replay message = %q, want compaction summary first", replay[0].Content)
	}
	if got := replay[1].Content; !strings.Contains(got, "current question") {
		t.Fatalf("second replay message = %q, want preserved current user input", got)
	}
	for _, message := range replay {
		if strings.Contains(message.Content, "old question") || strings.Contains(message.Content, "old answer") {
			t.Fatalf("old visible history leaked into compacted prompt replay: %#v", replay)
		}
	}

	snapshot, _, _, err := (&Service{}).snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if got, want := len(snapshot.Entries), len(loaded.Entries); got != want {
		t.Fatalf("stream checkpoint entries = %d, want persisted %d", got, want)
	}
}

func TestConcurrentCompactionPersistenceDoesNotDropEntries(t *testing.T) {
	const (
		conversationID = "conversation-concurrent-compaction"
		requestID      = "request-current"
	)
	store := NewConversationFileStore(t.TempDir())
	if _, err := store.CreateConversation(
		conversationID,
		agentv1.AgentMode_AGENT_MODE_AGENT,
		"",
		"",
		conversationID,
	); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	originalEntries := []HistoryEntry{
		compactionHistoryUserEntry(t, 1, "request-old", "message-old", "old question"),
		newAssistantTextEntry(1, "request-old", "old answer", "", ""),
		compactionHistoryUserEntry(t, 2, requestID, "message-current", "current question"),
	}
	conversation, _, err := store.AppendEntries(conversationID, originalEntries)
	if err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	plan := &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          requestID,
		CurrentUserText:           "current question",
		PreserveCurrentTurnInputs: true,
	}
	compactionEntries, err := buildCompactionStateEntries(conversation, plan, "stable summary")
	if err != nil {
		t.Fatalf("buildCompactionStateEntries() error = %v", err)
	}
	compacted := cloneConversationFile(conversation)
	if err := applyCompactionToConversation(compacted, plan, "stable summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	broker := NewStreamBroker()
	stream, err := broker.OpenStream(
		requestID,
		conversationID,
		2,
		"default",
		"default",
		agentv1.AgentMode_AGENT_MODE_AGENT,
		"current question",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	service := &Service{store: store}
	concurrentEntry := newMetadataEntry(2, requestID, "concurrent_event", map[string]any{"source": "second-agent"})

	start := make(chan struct{})
	errors := make(chan error, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		errors <- service.persistCompactionState(stream, conversationID, compacted, compactionEntries)
	}()
	go func() {
		defer group.Done()
		<-start
		_, _, appendErr := store.AppendEntries(conversationID, []HistoryEntry{concurrentEntry})
		errors <- appendErr
	}()
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent persistence error = %v", err)
		}
	}

	loaded, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if got, want := len(loaded.Entries), len(originalEntries)+len(compactionEntries)+1; got != want {
		t.Fatalf("persisted entries = %d, want %d", got, want)
	}
	seenCompaction := false
	seenConcurrent := false
	for _, entry := range loaded.Entries {
		switch entry.Kind {
		case "compacted_summary":
			seenCompaction = true
		case "metadata":
			var payload metadataPayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil && payload.Type == "concurrent_event" {
				seenConcurrent = true
			}
		}
	}
	if !seenCompaction || !seenConcurrent {
		t.Fatalf("concurrent persistence lost entries: compaction=%v concurrent=%v", seenCompaction, seenConcurrent)
	}
}

func compactionHistoryUserEntry(t *testing.T, turnSeq int64, requestID string, messageID string, text string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{Text: text, MessageId: messageID})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: requestID,
		Role:      "user",
		Kind:      "user_message",
		Payload:   payload,
	}
}
