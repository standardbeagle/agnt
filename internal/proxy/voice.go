package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
)

// VoiceSession manages a single voice transcription session between browser and Deepgram.
type VoiceSession struct {
	id            string
	browserConn   wsJSONWriter
	deepgramConn  *websocket.Conn
	mu            sync.Mutex
	closed        bool
	keepAliveDone chan struct{}
}

// DeepgramConfig holds configuration for Deepgram connection.
type DeepgramConfig struct {
	Model          string
	Language       string
	Punctuate      bool
	SmartFormat    bool
	InterimResults bool
	Encoding       string
	SampleRate     int
	Channels       int
}

// DefaultDeepgramConfig returns sensible defaults for voice transcription.
func DefaultDeepgramConfig() DeepgramConfig {
	return DeepgramConfig{
		Model:          "nova-3",
		Language:       "en",
		Punctuate:      true,
		SmartFormat:    true,
		InterimResults: true,
		Encoding:       "linear16",
		SampleRate:     16000,
		Channels:       1,
	}
}

// DeepgramMessage represents a message from Deepgram.
type DeepgramMessage struct {
	Type    string `json:"type"`
	Channel struct {
		Alternatives []struct {
			Transcript string  `json:"transcript"`
			Confidence float64 `json:"confidence"`
			Words      []struct {
				Word       string  `json:"word"`
				Start      float64 `json:"start"`
				End        float64 `json:"end"`
				Confidence float64 `json:"confidence"`
			} `json:"words"`
		} `json:"alternatives"`
	} `json:"channel"`
	IsFinal     bool    `json:"is_final"`
	SpeechFinal bool    `json:"speech_final"`
	Start       float64 `json:"start"`
	Duration    float64 `json:"duration"`
}

// VoiceTranscript is sent back to the browser.
type VoiceTranscript struct {
	Type       string  `json:"type"`
	Transcript string  `json:"transcript"`
	IsFinal    bool    `json:"is_final"`
	Confidence float64 `json:"confidence"`
	Start      float64 `json:"start"`
	Duration   float64 `json:"duration"`
}

// getDeepgramAPIKey retrieves the API key from environment.
func getDeepgramAPIKey() string {
	return os.Getenv("DEEPGRAM_API_KEY")
}

// NewVoiceSession creates a new voice session and connects to Deepgram.
func NewVoiceSession(id string, browserConn wsJSONWriter, config DeepgramConfig) (*VoiceSession, error) {
	apiKey := getDeepgramAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("DEEPGRAM_API_KEY environment variable not set")
	}

	// Build Deepgram WebSocket URL
	params := url.Values{}
	params.Set("model", config.Model)
	params.Set("language", config.Language)
	params.Set("punctuate", fmt.Sprintf("%t", config.Punctuate))
	params.Set("smart_format", fmt.Sprintf("%t", config.SmartFormat))
	params.Set("interim_results", fmt.Sprintf("%t", config.InterimResults))
	params.Set("encoding", config.Encoding)
	params.Set("sample_rate", fmt.Sprintf("%d", config.SampleRate))
	params.Set("channels", fmt.Sprintf("%d", config.Channels))

	wsURL := "wss://api.deepgram.com/v1/listen?" + params.Encode()

	// Connect to Deepgram with token authentication
	dialer := websocket.Dialer{
		Subprotocols: []string{"token", apiKey},
	}

	deepgramConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Deepgram: %w", err)
	}

	vs := &VoiceSession{
		id:            id,
		browserConn:   browserConn,
		deepgramConn:  deepgramConn,
		keepAliveDone: make(chan struct{}),
	}

	// Start goroutine to read Deepgram responses
	go vs.readDeepgramResponses()

	// Start keepalive
	go vs.keepAlive()

	return vs, nil
}

// deepgramWriteTimeout bounds a write to Deepgram. vs.mu serializes writes to
// the socket (gorilla forbids concurrent writers), so a write that blocks
// forever — a half-open connection, or a peer whose receive window never opens —
// holds the lock forever. Close() takes the same lock, so the voice session
// could never be torn down and its browser connection slot never released.
const deepgramWriteTimeout = 10 * time.Second

// SendAudio forwards audio data to Deepgram.
func (vs *VoiceSession) SendAudio(data []byte) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if vs.closed {
		return fmt.Errorf("session closed")
	}

	if err := vs.deepgramConn.SetWriteDeadline(time.Now().Add(deepgramWriteTimeout)); err != nil {
		return err
	}
	return vs.deepgramConn.WriteMessage(websocket.BinaryMessage, data)
}

// Close terminates the voice session.
func (vs *VoiceSession) Close() {
	vs.mu.Lock()
	if vs.closed {
		vs.mu.Unlock()
		return
	}
	vs.closed = true
	vs.mu.Unlock()

	// Stop keepalive
	close(vs.keepAliveDone)

	// Send close message to Deepgram
	closeMsg := map[string]string{"type": "CloseStream"}
	if data, err := json.Marshal(closeMsg); err == nil {
		if wErr := vs.deepgramConn.WriteMessage(websocket.TextMessage, data); wErr != nil {
			debug.Error("voice", "failed to send close stream: %v", wErr)
		}
	}

	vs.deepgramConn.Close()
}

// readDeepgramResponses reads transcription results from Deepgram and forwards to browser.
func (vs *VoiceSession) readDeepgramResponses() {
	defer vs.Close()

	for {
		_, message, err := vs.deepgramConn.ReadMessage()
		if err != nil {
			// Connection closed
			vs.sendToBrowser(map[string]interface{}{
				"type":  "voice_error",
				"error": "Deepgram connection closed",
			})
			return
		}

		var dgMsg DeepgramMessage
		if err := json.Unmarshal(message, &dgMsg); err != nil {
			// Best-effort: Deepgram emits message types we do not model; skip
			// frames we cannot decode rather than tearing down the session.
			debug.Log("proxy", "failed to parse Deepgram message: %v", err)
			continue
		}

		switch dgMsg.Type {
		case "Results":
			if len(dgMsg.Channel.Alternatives) > 0 {
				alt := dgMsg.Channel.Alternatives[0]
				if alt.Transcript != "" {
					vs.sendToBrowser(VoiceTranscript{
						Type:       "voice_transcript",
						Transcript: alt.Transcript,
						IsFinal:    dgMsg.IsFinal,
						Confidence: alt.Confidence,
						Start:      dgMsg.Start,
						Duration:   dgMsg.Duration,
					})
				}
			}

		case "Metadata":
			// Connection established
			vs.sendToBrowser(map[string]interface{}{
				"type":    "voice_ready",
				"message": "Deepgram connected",
			})

		case "SpeechStarted":
			vs.sendToBrowser(map[string]interface{}{
				"type": "voice_speech_started",
			})

		case "UtteranceEnd":
			vs.sendToBrowser(map[string]interface{}{
				"type": "voice_utterance_end",
			})

		case "Error":
			vs.sendToBrowser(map[string]interface{}{
				"type":    "voice_error",
				"error":   "Deepgram error",
				"details": string(message),
			})
		}
	}
}

// sendToBrowser sends a message to the browser WebSocket.
func (vs *VoiceSession) sendToBrowser(msg interface{}) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if vs.closed {
		return
	}

	if err := vs.browserConn.WriteJSON(msg); err != nil {
		debug.Error("voice", "failed to send transcription to browser: %v", err)
	}
}

// keepAlive sends periodic keepalive messages to Deepgram.
func (vs *VoiceSession) keepAlive() {
	ticker := time.NewTicker(8 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-vs.keepAliveDone:
			return
		case <-ticker.C:
			vs.mu.Lock()
			if !vs.closed {
				msg := map[string]string{"type": "KeepAlive"}
				if data, err := json.Marshal(msg); err == nil {
					if dErr := vs.deepgramConn.SetWriteDeadline(time.Now().Add(deepgramWriteTimeout)); dErr != nil {
						debug.Error("voice", "failed to set keepalive write deadline: %v", dErr)
					} else if wErr := vs.deepgramConn.WriteMessage(websocket.TextMessage, data); wErr != nil {
						debug.Error("voice", "failed to send keepalive to Deepgram: %v", wErr)
					}
				}
			}
			vs.mu.Unlock()
		}
	}
}
