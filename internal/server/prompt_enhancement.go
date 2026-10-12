package server

import "net/http"

// handleEnhancePrompt 不保存草稿或发出会话消息；只返回调用方可选择应用的纯文本。
func (s *Server) handleEnhancePrompt(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Text     string `json:"text"`
		Draft    string `json:"draft,omitempty"`
		Feedback string `json:"feedback,omitempty"`
	}
	if err := decodeWorkbenchJSON(r, &input, "text"); err != nil {
		return err
	}
	text, err := s.deps.Sessions.EnhancePromptWithFeedback(r.Context(), r.PathValue("id"), input.Text, input.Draft, input.Feedback)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Text string `json:"text"`
	}{text})
	return nil
}
