// Package notify sends notifications via signal-cli-rest-api, to the number itself. Off if
// SIGNAL_API_URL or SIGNAL_NUMBER is missing. A notification must never bring anything down, and in
// dry run nothing is sent.
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

// Notifier sends notifications in the background.
type Notifier struct {
	URL    string
	Number string
	DryRun bool
	HTTP   *http.Client
	Log    *logx.Logger

	wg sync.WaitGroup
}

// Send sends the message without waiting for the response.
func (n *Notifier) Send(message string) {
	text := "masto-rss: " + message
	if n.URL == "" || n.Number == "" {
		return
	}
	if n.DryRun {
		n.Log.Printf("[dry run] would notify: %s", text)
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := n.send(text); err != nil {
			n.Log.Printf("Could not send Signal notification: %s", netx.Describe(err))
		}
	}()
}

// Wait waits until all notifications have been sent (or given up).
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
