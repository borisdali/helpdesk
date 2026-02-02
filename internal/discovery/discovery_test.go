package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
)

func validAgentCard(name string) a2a.AgentCard {
	return a2a.AgentCard{
		Name:        name,
		Description: "Test agent",
		URL:         "http://should-be-overridden/invoke",
		Skills: []a2a.AgentSkill{
			{ID: name + "-skill", Name: "test", Description: "test skill"},
		},
	}
}

func agentCardServer(t *testing.T, card a2a.AgentCard) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(card)
	})
	return httptest.NewServer(mux)
}

func TestDiscover_Success(t *testing.T) {
	card := validAgentCard("test-agent")
	srv := agentCardServer(t, card)
	defer srv.Close()

	agents, err := Discover([]string{srv.URL})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}

	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}

	agent, ok := agents["test-agent"]
	if !ok {
		t.Fatal("expected agent named 'test-agent'")
	}

	if agent.Name != "test-agent" {
		t.Errorf("agent.Name = %q, want %q", agent.Name, "test-agent")
	}

	// InvokeURL should use the discovery base URL, not the card's self-reported URL.
	want := srv.URL + "/invoke"
	if agent.InvokeURL != want {
		t.Errorf("agent.InvokeURL = %q, want %q", agent.InvokeURL, want)
	}

	// Card URL should also be overridden.
	if agent.Card.URL != want {
		t.Errorf("agent.Card.URL = %q, want %q", agent.Card.URL, want)
	}
}

func TestDiscover_MultipleAgents(t *testing.T) {
	srv1 := agentCardServer(t, validAgentCard("agent-a"))
	defer srv1.Close()
	srv2 := agentCardServer(t, validAgentCard("agent-b"))
	defer srv2.Close()

	agents, err := Discover([]string{srv1.URL, srv2.URL})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if _, ok := agents["agent-a"]; !ok {
		t.Error("missing agent-a")
	}
	if _, ok := agents["agent-b"]; !ok {
		t.Error("missing agent-b")
	}
}

func TestDiscover_UnreachableURLSkipped(t *testing.T) {
	good := agentCardServer(t, validAgentCard("good-agent"))
	defer good.Close()

	agents, err := Discover([]string{"http://127.0.0.1:1", good.URL})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (bad URL skipped), got %d", len(agents))
	}
	if _, ok := agents["good-agent"]; !ok {
		t.Error("missing good-agent")
	}
}

func TestDiscover_InvalidJSONSkipped(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer badSrv.Close()

	goodSrv := agentCardServer(t, validAgentCard("good-agent"))
	defer goodSrv.Close()

	agents, err := Discover([]string{badSrv.URL, goodSrv.URL})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (bad JSON skipped), got %d", len(agents))
	}
}

func TestDiscover_Non200Skipped(t *testing.T) {
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()

	goodSrv := agentCardServer(t, validAgentCard("good-agent"))
	defer goodSrv.Close()

	agents, err := Discover([]string{badSrv.URL, goodSrv.URL})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (500 skipped), got %d", len(agents))
	}
}

func TestDiscover_AllFailReturnsError(t *testing.T) {
	_, err := Discover([]string{"http://127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected error when all URLs fail, got nil")
	}
}

func TestDiscover_TrailingSlashStripped(t *testing.T) {
	srv := agentCardServer(t, validAgentCard("slash-agent"))
	defer srv.Close()

	agents, err := Discover([]string{srv.URL + "/"})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	want := srv.URL + "/invoke"
	if agents["slash-agent"].InvokeURL != want {
		t.Errorf("InvokeURL = %q, want %q", agents["slash-agent"].InvokeURL, want)
	}
}
