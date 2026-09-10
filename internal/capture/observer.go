package capture

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"
)

type BodyPolicy struct {
	MaxBodyBytes  int64
	MaxTotalBytes int64
	URLPattern    *regexp.Regexp
	// DisableHTTPBodies stops the observer from ever requesting HTTP response
	// bodies (getResponseBody). Metadata, loadingFinished/Failed and every
	// already-received CDP message are still recorded unchanged; this is a
	// pre-fetch selection like selected(), not a discard of received raw.
	DisableHTTPBodies bool
}

func (p BodyPolicy) selected(e Event) bool {
	if p.URLPattern != nil && p.URLPattern.MatchString(e.URL) {
		return true
	}
	typ, _, _ := mime.ParseMediaType(e.MIMEType)
	typ = strings.ToLower(typ)
	if strings.HasPrefix(typ, "image/") || strings.HasPrefix(typ, "audio/") || strings.HasPrefix(typ, "video/") || strings.HasPrefix(typ, "font/") {
		return false
	}
	return e.ResourceType == "XHR" || e.ResourceType == "Fetch" || typ == "application/json" || typ == "application/octet-stream" || strings.Contains(typ, "protobuf") || strings.HasSuffix(typ, "+json")
}

type Sink interface {
	Append(Event) (Event, error)
}

type bodyRequest struct {
	event    Event
	deadline time.Time
}

// observer is deliberately independent of Chrome and game protocol decoding.
type observer struct {
	sink      Sink
	policy    BodyPolicy
	after     func(Event)
	send      func(string, any) (int64, error)
	responses map[string]Event
	pending   map[int64]bodyRequest
	bodyBytes int64
}

func newObserver(sink Sink, p BodyPolicy, after func(Event), send func(string, any) (int64, error)) *observer {
	return &observer{sink: sink, policy: p, after: after, send: send, responses: map[string]Event{}, pending: map[int64]bodyRequest{}}
}

func (o *observer) emit(e Event) error {
	saved, err := o.sink.Append(e)
	if err != nil {
		return err
	}
	if o.after != nil {
		o.after(saved)
	}
	return nil
}

func (o *observer) handle(msg *cdproto.Message, commands bool) error {
	if msg.Method == "" {
		if req, ok := o.pending[msg.ID]; ok {
			delete(o.pending, msg.ID)
			return o.body(req.event, msg)
		}
		// Late/unmatched command replies are retained, including exact body data.
		if msg.ID != 0 {
			e := Event{Kind: "cdp_reply", CDPID: msg.ID, CDPResult: json.RawMessage(msg.Result)}
			if msg.Error != nil {
				e.Error = msg.Error.Error()
			}
			return o.emit(e)
		}
		return nil
	}
	e := Event{Kind: "cdp_event", CDPMethod: string(msg.Method), CDPParams: json.RawMessage(msg.Params)}
	var common struct {
		RequestID string `json:"requestId"`
		URL       string `json:"url"`
	}
	if err := json.Unmarshal(msg.Params, &common); err != nil {
		e.Error = "invalid CDP event params; original retained"
		return o.emit(e)
	}
	e.RequestID = common.RequestID
	switch string(msg.Method) {
	case "Network.webSocketFrameSent", "Network.webSocketFrameReceived":
		e.ConnectionID = common.RequestID
		e.Direction = "received"
		if string(msg.Method) == "Network.webSocketFrameSent" {
			e.Direction = "sent"
		}
		var frame struct {
			Response *network.WebSocketFrame `json:"response"`
		}
		if err := json.Unmarshal(msg.Params, &frame); err != nil || frame.Response == nil || common.RequestID == "" || frame.Response.Opcode != math.Trunc(frame.Response.Opcode) || frame.Response.Opcode < 0 || frame.Response.Opcode > 15 {
			e.Error = "invalid websocket frame metadata; original retained"
			return o.emit(e)
		}
		e.Opcode = int64(frame.Response.Opcode)
		data := []byte(frame.Response.PayloadData)
		// CDP specifies opcode 1 as UTF-8; all other opcodes use base64.
		if e.Opcode != 1 {
			var err error
			data, err = base64.StdEncoding.DecodeString(frame.Response.PayloadData)
			if err != nil {
				e.Error = "invalid CDP websocket base64; original retained"
				return o.emit(e)
			}
		}
		e.Kind, e.PayloadHex = "websocket", hex.EncodeToString(data)
	case "Network.webSocketCreated":
		e.Kind, e.ConnectionID, e.URL = "websocket_open", common.RequestID, common.URL
	case "Network.webSocketClosed":
		e.Kind, e.ConnectionID = "websocket_close", common.RequestID
	case "Network.responseReceived":
		var meta struct {
			Type     string `json:"type"`
			Response *struct {
				URL    string  `json:"url"`
				Status float64 `json:"status"`
				MIME   string  `json:"mimeType"`
			} `json:"response"`
		}
		if err := json.Unmarshal(msg.Params, &meta); err != nil || meta.Response == nil || common.RequestID == "" || meta.Response.Status != math.Trunc(meta.Response.Status) || meta.Response.Status < 0 || meta.Response.Status > 999 {
			e.Error = "invalid HTTP metadata; original retained"
			return o.emit(e)
		}
		e.Kind, e.URL, e.Status, e.MIMEType, e.ResourceType = "http_metadata", meta.Response.URL, int64(meta.Response.Status), meta.Response.MIME, meta.Type
		e.BodyStatus = "not_selected"
		if o.policy.DisableHTTPBodies {
			// Takes precedence over URLPattern/selected: no body request is
			// ever issued and nothing is tracked for a later fetch.
			e.BodyStatus = "disabled"
		} else if o.policy.selected(e) {
			e.BodyStatus = "awaiting_loading_finished"
			if len(o.responses) >= 4096 {
				e.BodyStatus = "metadata_limit"
			} else {
				o.responses[common.RequestID] = e
			}
		}
	case "Network.loadingFinished":
		e.Kind = "http_finished"
		// Persist loadingFinished before making the only permitted body call.
		if err := o.emit(e); err != nil {
			return err
		}
		meta, ok := o.responses[common.RequestID]
		if !ok {
			return nil
		}
		delete(o.responses, common.RequestID)
		meta.CDPMethod, meta.CDPParams, meta.Kind = "", nil, "http_response"
		var finished struct {
			EncodedDataLength float64 `json:"encodedDataLength"`
		}
		if err := json.Unmarshal(msg.Params, &finished); err != nil || finished.EncodedDataLength < 0 {
			meta.BodyStatus = "invalid_loading_finished"
		} else if !commands {
			meta.BodyStatus = "interrupted"
		} else if finished.EncodedDataLength > float64(o.policy.MaxBodyBytes) {
			meta.BodyStatus = "encoded_size_limit"
		} else if o.bodyBytes+int64(len(o.pending)+1)*o.policy.MaxBodyBytes > o.policy.MaxTotalBytes {
			meta.BodyStatus = "body_budget_limit"
		} else {
			id, err := o.send(network.CommandGetResponseBody, network.GetResponseBody(network.RequestID(common.RequestID)))
			if err != nil {
				return err
			}
			meta.CDPID = id
			o.pending[id] = bodyRequest{event: meta, deadline: time.Now().Add(30 * time.Second)}
			return nil
		}
		return o.emit(meta)
	case "Network.loadingFailed":
		e.Kind, e.BodyStatus = "http_failed", "loading_failed"
		delete(o.responses, common.RequestID)
	}
	return o.emit(e)
}

func (o *observer) body(e Event, msg *cdproto.Message) error {
	e.Kind, e.CDPResult = "http_response", json.RawMessage(msg.Result)
	if msg.Error != nil {
		e.BodyStatus, e.Error = "body_unavailable", msg.Error.Error()
		return o.emit(e)
	}
	var body struct {
		Body          *string `json:"body"`
		Base64Encoded bool    `json:"base64Encoded"`
	}
	if err := json.Unmarshal(msg.Result, &body); err != nil || body.Body == nil {
		e.BodyStatus = "invalid_body_result"
		return o.emit(e)
	}
	data := []byte(*body.Body)
	if body.Base64Encoded {
		var err error
		data, err = base64.StdEncoding.DecodeString(*body.Body)
		if err != nil {
			e.BodyStatus = "invalid_body_encoding"
			return o.emit(e)
		}
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	e.BodyBase64, e.BodyStatus = &encoded, "stored"
	o.bodyBytes += int64(len(data))
	if int64(len(data)) > o.policy.MaxBodyBytes {
		// Once Chrome returned bytes, preserve all of them, even on overshoot.
		e.BodyStatus = "stored_over_limit"
	}
	return o.emit(e)
}

func (o *observer) expire(now time.Time, all bool) error {
	for id, req := range o.pending {
		if all || !now.Before(req.deadline) {
			req.event.Kind, req.event.BodyStatus = "http_response", "body_timeout"
			if all {
				req.event.BodyStatus = "interrupted"
			}
			if err := o.emit(req.event); err != nil {
				return err
			}
			delete(o.pending, id)
		}
	}
	if all {
		for id, e := range o.responses {
			e.Kind, e.BodyStatus = "http_response", "loading_not_observed_before_stop"
			if err := o.emit(e); err != nil {
				return err
			}
			delete(o.responses, id)
		}
	}
	return nil
}

// sendReadOnly is the single CDP write boundary. No browser actions or arbitrary
// command execution are exposed, even if a caller passes a forbidden method.
func sendReadOnly(ctx context.Context, t transport, id int64, method string, params any) error {
	if method != network.CommandEnable && method != network.CommandGetResponseBody {
		return fmt.Errorf("CDP command rejected by read-only policy")
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode CDP observation parameters: %w", err)
	}
	if err := t.Write(ctx, &cdproto.Message{ID: id, Method: cdproto.MethodType(method), Params: raw}); err != nil {
		return fmt.Errorf("CDP observation command %s failed: %w", method, err)
	}
	return nil
}
