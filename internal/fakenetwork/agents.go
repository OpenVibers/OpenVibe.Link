package fakenetwork

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/OpenVibers/OpenVibe.Link/internal/access"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

type fakeAgent struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	LocalID   string `json:"local_id"`
	Name      string `json:"name"`
	MachineID string `json:"machine_id"`
	Owner     string `json:"owner"`
	Status    string `json:"status"`
}

type fakeAgentTask struct {
	ID          string          `json:"id"`
	To          string          `json:"to"`
	From        string          `json:"from"`
	OnBehalfOf  string          `json:"on_behalf_of"`
	Thread      string          `json:"thread"`
	Message     json.RawMessage `json:"message"`
	CreatedAt   string          `json:"created_at"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	fromMachine string
	toMachine   string
}

func (s *Server) registerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/agents/register", s.agentRegister)
	mux.HandleFunc("/api/v1/link/self/agents", s.agentList)
	mux.HandleFunc("/api/v1/agents/", s.agentRoute)
}

func (s *Server) agentRegister(w http.ResponseWriter, r *http.Request) {
	principal := s.accessPrincipal(r)
	if principal == "" || r.Method != http.MethodPost {
		problem(w, 403, "agents.node_required")
		return
	}
	var body fakeAgent
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Kind != "link-local" || body.LocalID == "" || body.Name == "" || body.MachineID != principal {
		problem(w, 400, "agents.invalid_registration")
		return
	}
	s.mu.Lock()
	for _, a := range s.agents {
		if a.MachineID == principal && a.LocalID == body.LocalID {
			a.Status = body.Status
			result := *a
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(result)
			return
		}
	}
	body.ID = "agt_" + random()[:16]
	body.Owner = fakeOwner
	s.agents[body.ID] = &body
	s.mu.Unlock()
	w.WriteHeader(201)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) agentList(w http.ResponseWriter, r *http.Request) {
	if s.accessPrincipal(r) == "" || r.Method != http.MethodGet {
		problem(w, 403, "agents.node_required")
		return
	}
	s.mu.Lock()
	list := make([]fakeAgent, 0, len(s.agents))
	for _, a := range s.agents {
		list = append(list, *a)
	}
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"agents": list})
}

func (s *Server) agentRoute(w http.ResponseWriter, r *http.Request) {
	principal := s.accessPrincipal(r)
	if principal == "" {
		problem(w, 403, "agents.node_required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/agents/"), "/")
	if len(parts) == 2 && parts[1] == "tasks" && r.Method == http.MethodPost {
		var body struct {
			From, OnBehalfOf, Thread string
			Message                  json.RawMessage
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.From == "" || body.Thread == "" || len(body.Message) == 0 {
			problem(w, 400, "agents.invalid_task")
			return
		}
		s.mu.Lock()
		dest := s.agents[parts[0]]
		sender := s.agents[body.From]
		if dest == nil || sender == nil || sender.MachineID != principal || dest.Status == "offline" {
			s.mu.Unlock()
			problem(w, 403, "agents.not_allowed")
			return
		}
		task := &fakeAgentTask{ID: access.NewTaskID(), To: dest.ID, From: sender.ID, OnBehalfOf: fakeOwner, Thread: body.Thread, Message: body.Message, CreatedAt: time.Now().UTC().Format(time.RFC3339), Status: "pending", fromMachine: principal, toMachine: dest.MachineID}
		s.agentTasks[task.ID] = task
		s.mu.Unlock()
		payload, _ := json.Marshal(task)
		if s.bot == nil || !s.bot.DeliverAgentTask(dest.MachineID, protocol.AgentTaskFrame{Task: payload}) {
			problem(w, 503, "agents.target_offline")
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(task)
		return
	}
	if len(parts) >= 2 && parts[0] == "tasks" {
		s.mu.Lock()
		task := s.agentTasks[parts[1]]
		if task == nil || principal != task.fromMachine && principal != task.toMachine {
			s.mu.Unlock()
			problem(w, 404, "agents.task_missing")
			return
		}
		if len(parts) == 2 && r.Method == http.MethodGet {
			copy := *task
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(copy)
			return
		}
		if len(parts) == 3 && parts[2] == "result" && r.Method == http.MethodPost && principal == task.toMachine {
			var body struct {
				Status string          `json:"status"`
				Result json.RawMessage `json:"result"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Status != "completed" || len(body.Result) == 0 {
				s.mu.Unlock()
				problem(w, 400, "agents.invalid_result")
				return
			}
			task.Status = "completed"
			task.Result = body.Result
			copy := *task
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(copy)
			return
		}
		s.mu.Unlock()
	}
	w.WriteHeader(404)
}
