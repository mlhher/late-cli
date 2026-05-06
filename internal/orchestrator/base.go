package orchestrator

import (
	"context"
	"late/internal/archive"
	"late/internal/client"
	"late/internal/common"
	"late/internal/config"
	"late/internal/executor"
	"late/internal/session"
	"late/internal/tool"
	"log"
	"os"
	"sync"
	"time"
)

// BaseOrchestrator implements common.Orchestrator and manages an agent's run loop.
type BaseOrchestrator struct {
	id          string
	sess        *session.Session
	middlewares []common.ToolMiddleware
	eventCh     chan common.Event

	mu       sync.RWMutex
	parent   common.Orchestrator
	children []common.Orchestrator

	// Running state tracker
	acc    executor.StreamAccumulator
	ctx    context.Context
	cancel context.CancelFunc

	// Stop mechanism
	stopCh chan struct{}

	// Max turns configuration
	maxTurns int

	// Archive subsystem (nil when compaction is disabled)
	archiveSub *archiveState
}

// archiveState holds loaded archive and search service for one session run.
type archiveState struct {
	sub *tool.ArchiveSubsystem
	cfg config.ArchiveCompactionConfig
}

func NewBaseOrchestrator(id string, sess *session.Session, middlewares []common.ToolMiddleware, maxTurns int) *BaseOrchestrator {
	return &BaseOrchestrator{
		id:          id,
		sess:        sess,
		middlewares: middlewares,
		eventCh:     make(chan common.Event, 100),
		ctx:         context.Background(),
		stopCh:      make(chan struct{}),
		maxTurns:    maxTurns,
	}
}

func (o *BaseOrchestrator) SetMiddlewares(middlewares []common.ToolMiddleware) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.middlewares = middlewares
}

func (o *BaseOrchestrator) SetContext(ctx context.Context) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ctx = ctx
}

func (o *BaseOrchestrator) SetMaxTurns(maxTurns int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.maxTurns = maxTurns
}

func (o *BaseOrchestrator) MaxTokens() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.sess.Client().ContextSize()
}

func (o *BaseOrchestrator) RefreshContextSize(ctx context.Context) {
	o.sess.Client().RefreshContextSize(ctx)
}

func (o *BaseOrchestrator) ID() string { return o.id }

func (o *BaseOrchestrator) Submit(text string) error {
	o.mu.Lock()
	// Clear any old cancellation state so a new run isn't instantly aborted
	o.cancel = nil
	// Reset the base context if it was already cancelled
	if o.ctx.Err() != nil {
		o.ctx = context.Background()
	}
	o.mu.Unlock()

	if err := o.sess.AddUserMessage(text); err != nil {
		return err
	}

	o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	// Start the run loop in a background goroutine
	go o.run()
	return nil
}

func (o *BaseOrchestrator) Execute(text string) (string, error) {
	o.mu.Lock()
	if o.ctx.Err() != nil {
		o.ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel = cancel
	o.ctx = ctx // Set the Context for this execution
	o.mu.Unlock()

	defer cancel()

	// Inject orchestrator ID into context for tool interactions
	ctx = context.WithValue(ctx, common.OrchestratorIDKey, o.id)

	if err := o.sess.AddUserMessage(text); err != nil {
		return "", err
	}

	o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	defer func() {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
	}()

	// Build extra body
	var extraBody map[string]any

	// Pre-run archive compaction hook (fail-open).
	o.runArchivePreHook()

	onStartTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	}

	onEndTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		usage := o.acc.Usage
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.ContentEvent{ID: o.id, Usage: usage}
	}

	res, err := executor.RunLoop(
		ctx,
		o.sess,
		o.maxTurns,
		extraBody,
		onStartTurn,
		onEndTurn,
		func(res common.StreamResult) {
			o.mu.Lock()
			o.acc.Append(res)
			accCopy := o.acc
			o.mu.Unlock()

			o.eventCh <- common.ContentEvent{
				ID:               o.id,
				Content:          accCopy.Content,
				ReasoningContent: accCopy.Reasoning,
				ToolCalls:        accCopy.ToolCalls,
				Usage:            accCopy.Usage,
			}
		},
		o.middlewares,
	)

	if err != nil {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: err}
	} else {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "closed"}
	}
	return res, err
}

func (o *BaseOrchestrator) run() {
	// Build extra body
	var extraBody map[string]any

	o.mu.Lock()
	if o.ctx.Err() != nil {
		o.ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.cancel = cancel
	o.ctx = ctx // Set the context so Execute/RunLoop can share the cancelable context safely
	o.mu.Unlock()

	defer cancel() // Ensure we don't leak the context when run() finishes

	// Inject orchestrator ID into context for tool interactions
	ctx = context.WithValue(ctx, common.OrchestratorIDKey, o.id)

	// Pre-run archive compaction hook (fail-open).
	o.runArchivePreHook()

	onStartTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "thinking"}
	}

	onEndTurn := func() {
		o.RefreshContextSize(ctx)
		o.mu.Lock()
		usage := o.acc.Usage
		o.acc.Reset()
		o.mu.Unlock()
		o.eventCh <- common.ContentEvent{ID: o.id, Usage: usage}
	}

	_, err := executor.RunLoop(
		ctx,
		o.sess,
		o.maxTurns,
		extraBody,
		onStartTurn,
		onEndTurn,
		func(res common.StreamResult) {
			o.mu.Lock()
			o.acc.Append(res)
			accCopy := o.acc // Copy for event
			o.mu.Unlock()

			o.eventCh <- common.ContentEvent{
				ID:               o.id,
				Content:          accCopy.Content,
				ReasoningContent: accCopy.Reasoning,
				ToolCalls:        accCopy.ToolCalls,
				Usage:            accCopy.Usage,
			}
		},
		o.middlewares,
	)

	// Reset accumulator after finished or ready for next turn
	o.mu.Lock()
	o.acc.Reset()
	o.mu.Unlock()

	if err != nil {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "error", Error: err}
	} else {
		o.eventCh <- common.StatusEvent{ID: o.id, Status: "idle"}
	}

	// Check if stop was requested and send StopRequestedEvent
	if o.IsStopRequested() {
		o.eventCh <- common.StopRequestedEvent{ID: o.id}
	}
}

func (o *BaseOrchestrator) Events() <-chan common.Event {
	return o.eventCh
}

func (o *BaseOrchestrator) Cancel() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.cancel != nil {
		o.cancel()
	}

	select {
	case o.stopCh <- struct{}{}:
		// Signal sent
	default:
		// Already signaled, ignore
	}
}

func (o *BaseOrchestrator) IsStopRequested() bool {
	select {
	case <-o.stopCh:
		return true
	default:
		return false
	}
}

func (o *BaseOrchestrator) History() []client.ChatMessage {
	return o.sess.History
}

func (o *BaseOrchestrator) Session() *session.Session {
	return o.sess
}

func (o *BaseOrchestrator) SystemPrompt() string {
	return o.sess.SystemPrompt()
}

func (o *BaseOrchestrator) ToolDefinitions() []client.ToolDefinition {
	return o.sess.GetToolDefinitions()
}

func (o *BaseOrchestrator) Context() context.Context {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.ctx
}

func (o *BaseOrchestrator) Middlewares() []common.ToolMiddleware {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.middlewares
}

func (o *BaseOrchestrator) Registry() *common.ToolRegistry {
	return o.sess.Registry
}

func (o *BaseOrchestrator) Children() []common.Orchestrator {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.children
}

func (o *BaseOrchestrator) Parent() common.Orchestrator {
	return o.parent
}

func (o *BaseOrchestrator) Reset() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sess.History = []client.ChatMessage{}
	return session.SaveHistory(o.sess.HistoryPath, nil)
}

func (o *BaseOrchestrator) AddChild(child common.Orchestrator) {
	o.mu.Lock()
	o.children = append(o.children, child)
	o.mu.Unlock()

	o.eventCh <- common.ChildAddedEvent{
		ParentID: o.id,
		Child:    child,
	}
}

// runArchivePreHook runs archive compaction before a run loop if enabled.
// Fail-open: any error is logged but does not block execution.
func (o *BaseOrchestrator) runArchivePreHook() {
	histPath := o.sess.HistoryPath
	if histPath == "" {
		return
	}

	cfg, err := config.LoadConfig()
	if err != nil || !cfg.IsArchiveCompactionEnabled() {
		return
	}
	settings := cfg.ArchiveCompactionSettings()

	// Phase 8: verify archive file permissions (warn only).
	archPath := archive.ArchivePath(histPath)
	if info, statErr := os.Stat(archPath); statErr == nil {
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			log.Printf("[archive] warning: archive file %s has loose permissions (%o); expected 0600", archPath, perm)
		}
	}

	var arch *archive.SessionArchive
	o.mu.Lock()
	existing := o.archiveSub
	o.mu.Unlock()

	if existing != nil && existing.sub != nil && existing.sub.Archive != nil {
		arch = existing.sub.Archive
	} else {
		arch, err = archive.Load(archPath, o.id)
		if err != nil {
			log.Printf("[archive] failed to load archive for hook: %v", err)
			return
		}
	}

	compactCfg := archive.CompactionConfig{
		ThresholdMessages:  settings.CompactionThresholdMessages,
		KeepRecentMessages: settings.KeepRecentMessages,
		ChunkSize:          settings.ArchiveChunkSize,
		StaleAfterSeconds:  settings.LockStaleAfterSeconds,
	}

	log.Printf("[archive] pre-run hook: history=%d msgs, threshold=%d", len(o.sess.History), settings.CompactionThresholdMessages)
	compactStart := time.Now()

	res, newActive, newArch, err := archive.Compact(
		histPath, o.id,
		o.sess.History,
		arch,
		compactCfg,
	)
	compactDur := time.Since(compactStart)

	if err != nil {
		log.Printf("[archive] compaction hook error: %v", err)
		return
	}
	if res.LockHeld {
		log.Printf("[archive] compaction skipped (lock held by another process)")
	}
	if !res.NoOp {
		log.Printf("[archive] compaction complete: archived=%d msgs in %s", res.ArchivedCount, compactDur)
		o.mu.Lock()
		o.sess.History = newActive
		o.mu.Unlock()
		if err := session.SaveHistory(histPath, newActive); err != nil {
			log.Printf("[archive] failed to persist compacted history: %v", err)
		}

		// Phase 8: update session meta counters.
		metaID := archive.BaseSessionID(histPath)
		if meta, loadErr := session.LoadSessionMeta(metaID); loadErr == nil && meta != nil {
			meta.CompactionCount = newArch.CompactionCount
			meta.ArchivedMessageCount = newArch.ArchivedMessageCount
			meta.LastCompactionAt = time.Now().UTC()
			if saveErr := session.SaveSessionMeta(*meta); saveErr != nil {
				log.Printf("[archive] failed to save session meta counters: %v", saveErr)
			}
		}
	}

	svc := archive.NewSearchService(newArch)
	if !res.NoOp {
		svc.MarkDirty()
	}
	searchStart := time.Now()
	_ = svc.Search("", 0, false) // warm the lazy index
	log.Printf("[archive] search index ready in %s", time.Since(searchStart))

	o.mu.Lock()
	o.archiveSub = &archiveState{
		sub: &tool.ArchiveSubsystem{
			Archive: newArch,
			Search:  svc,
		},
		cfg: settings,
	}
	o.mu.Unlock()

	// Register archive tools into session registry (idempotent: only if not already present).
	reg := o.sess.Registry
	if reg != nil && reg.Get("search_session_archive") == nil {
		tool.RegisterArchiveTools(reg, o.archiveSub.sub,
			settings.ArchiveSearchMaxResults,
			settings.ArchiveSearchCaseSensitive)
		log.Printf("[archive] tools registered (search_session_archive, retrieve_archived_message)")
	}
}
