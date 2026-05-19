package models

type UserSession struct {
	State   string                 `json:"state"`
	Context map[string]interface{} `json:"context"`
}

func NewUserSession() UserSession {
	return UserSession{
		State:   "bot",
		Context: make(map[string]interface{}),
	}
}
