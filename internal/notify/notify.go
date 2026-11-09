// Package notify skickar aviseringar via signal-cli-rest-api, till numret självt. Av om
// SIGNAL_API_URL eller SIGNAL_NUMBER saknas. En avisering får aldrig fälla något, och i torrläge
// skickas inget.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/logx"
	"github.com/maxbengtzen/masto-rss/internal/netx"
)

// Notifier skickar aviseringar i bakgrunden.
type Notifier struct {
	URL    string
	Number string
	DryRun bool
	HTTP   *http.Client
	Log    *logx.Logger

	wg sync.WaitGroup
}

// Send skickar meddelandet utan att vänta på svaret.
func (n *Notifier) Send(message string) {
	text := "masto-rss: " + message
	if n.URL == "" || n.Number == "" {
		return
	}
	if n.DryRun {
		n.Log.Printf("[torrläge] skulle avisera: %s", text)
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := n.send(text); err != nil {
			n.Log.Printf("Kunde inte skicka Signal-avisering: %s", netx.Describe(err))
		}
	}()
}

// Wait väntar tills alla aviseringar skickats (eller gett upp).
func (n *Notifier) Wait() { n.wg.Wait() }

func (n *Notifier) send(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	enc.Encode(map[string]any{"message": text, "number": n.Number, "recipients": []string{n.Number}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL+"/v2/send", bytes.NewReader(bytes.TrimRight(body.Bytes(), "\n")))
	if err != nil {
		return netx.FetchError(err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := n.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return netx.FetchError(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		b, _ := io.ReadAll(res.Body)
		return netx.Errorf("%s", fmt.Sprintf("HTTP %d %s", res.StatusCode, js.Slice16(string(b), 0, 200)))
	}
	return nil
}
