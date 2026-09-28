package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSpawnSubagentTool_TimeoutParsing covers the per-spawn "timeout"
// argument: absent/empty → no override; a valid duration → parsed override;
// "0"/negative → explicit unlimited (pointer to 0); an invalid value → an
// error RESULT (nil Go error) with the runner never invoked, so the model
// can retry with a valid duration.
func TestSpawnSubagentTool_TimeoutParsing(t *testing.T) {
	tests := []struct {
		name         string
		args         string
		wantErrStr   bool           // expect an error-result string; runner not called
		wantOverride *time.Duration // expected override passed to the runner
	}{
		{
			name:         "absent timeout means no override",
			args:         `{"goal":"g","agent_type":"coder"}`,
			wantOverride: nil,
		},
		{
			name:         "empty timeout means no override",
			args:         `{"goal":"g","agent_type":"coder","timeout":""}`,
			wantOverride: nil,
		},
		{
			name:         "valid duration is passed through",
			args:         `{"goal":"g","agent_type":"coder","timeout":"45m"}`,
			wantOverride: subagentTimeoutPtr(45 * time.Minute),
		},
		{
			name:         "two hours is passed through",
			args:         `{"goal":"g","agent_type":"coder","timeout":"2h"}`,
			wantOverride: subagentTimeoutPtr(2 * time.Hour),
		},
		{
			name:         "zero means explicit unlimited",
			args:         `{"goal":"g","agent_type":"coder","timeout":"0"}`,
			wantOverride: subagentTimeoutPtr(0),
		},
		{
			name:         "negative means explicit unlimited normalized to 0",
			args:         `{"goal":"g","agent_type":"coder","timeout":"-5m"}`,
			wantOverride: subagentTimeoutPtr(0),
		},
		{
			name:       "garbage duration is an error result",
			args:       `{"goal":"g","agent_type":"coder","timeout":"banana"}`,
			wantErrStr: true,
		},
		{
			name:       "missing unit is an error result",
			args:       `{"goal":"g","agent_type":"coder","timeout":"5"}`,
			wantErrStr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runnerCalled := false
			var gotOverride *time.Duration
			spawnTool := SpawnSubagentTool{
				Runner: func(ctx context.Context, goal string, ctxFiles []string, agentType string, timeoutOverride *time.Duration) (string, error) {
					runnerCalled = true
					gotOverride = timeoutOverride
					if goal != "g" || agentType != "coder" {
						t.Errorf("runner got goal=%q agentType=%q, want goal=%q agentType=%q", goal, agentType, "g", "coder")
					}
					return "ok", nil
				},
			}

			result, err := spawnTool.Execute(context.Background(), json.RawMessage(tt.args))
			if err != nil {
				t.Fatalf("Execute() Go error = %v, want nil", err)
			}

			if tt.wantErrStr {
				if runnerCalled {
					t.Fatal("runner must not be invoked for an invalid timeout")
				}
				if !strings.Contains(result, `invalid subagent timeout`) || !strings.Contains(result, "use a duration like 45m, 2h") {
					t.Fatalf("error result = %q, want the invalid-timeout retry hint", result)
				}
				return
			}

			if !runnerCalled {
				t.Fatal("runner was not invoked")
			}
			if tt.wantOverride == nil {
				if gotOverride != nil {
					t.Fatalf("override = %v, want nil", *gotOverride)
				}
				return
			}
			if gotOverride == nil {
				t.Fatalf("override = nil, want %v", *tt.wantOverride)
			}
			if *gotOverride != *tt.wantOverride {
				t.Fatalf("override = %v, want %v", *gotOverride, *tt.wantOverride)
			}
		})
	}
}

// TestSpawnSubagentTool_ParametersDocumentTimeout guards the JSON schema:
// the optional timeout property and its budget semantics must stay advertised
// to the model.
func TestSpawnSubagentTool_ParametersDocumentTimeout(t *testing.T) {
	schema := string(SpawnSubagentTool{Runner: nil}.Parameters())
	if !strings.Contains(schema, `"timeout"`) {
		t.Fatal("parameters schema does not advertise the timeout property")
	}
	if !strings.Contains(schema, "unlimited") {
		t.Fatal("timeout schema description does not document the unlimited semantics")
	}
	if !strings.Contains(schema, "--subagent-timeout/config value") {
		t.Fatal("timeout schema description does not document the omitted-means-global semantics")
	}
}

func subagentTimeoutPtr(d time.Duration) *time.Duration { return &d }
