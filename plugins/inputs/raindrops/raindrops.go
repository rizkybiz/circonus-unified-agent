package raindrops

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/circonus-labs/circonus-unified-agent/cua"
	"github.com/circonus-labs/circonus-unified-agent/plugins/inputs"
)

type Raindrops struct {
	URLs       []string
	httpClient *http.Client
}

var sampleConfig = `
  ## An array of raindrops middleware URI to gather stats.
  urls = ["http://localhost:8080/_raindrops"]
`

func (r *Raindrops) SampleConfig() string {
	return sampleConfig
}

func (r *Raindrops) Description() string {
	return "Read raindrops stats (raindrops - real-time stats for preforking Rack servers)"
}

func (r *Raindrops) Gather(acc cua.Accumulator) error {
	var wg sync.WaitGroup

	for _, u := range r.URLs {
		addr, err := url.Parse(u)
		if err != nil {
			acc.AddError(fmt.Errorf("Unable to parse address '%s': %w", u, err))
			continue
		}

		wg.Add(1)
		go func(addr *url.URL) {
			defer wg.Done()
			acc.AddError(r.gatherURL(addr, acc))
		}(addr)
	}

	wg.Wait()

	return nil
}

func (r *Raindrops) gatherURL(addr *url.URL, acc cua.Accumulator) error {
	resp, err := r.httpClient.Get(addr.String())
	if err != nil {
		return fmt.Errorf("error making HTTP request to %s: %w", addr.String(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP status %s", addr.String(), resp.Status)
	}
	buf := bufio.NewReader(resp.Body)

	// Calling
	_, err = buf.ReadString(':')
	if err != nil {
		return fmt.Errorf("readstring: %w", err)
	}
	line, err := buf.ReadString('\n')
	if err != nil {
		return fmt.Errorf("readstring: %w", err)
	}
	calling, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
	if err != nil {
		return fmt.Errorf("parseuint (%s): %w", strings.TrimSpace(line), err)
	}

	// Writing
	_, err = buf.ReadString(':')
	if err != nil {
		return fmt.Errorf("readstring: %w", err)
	}
	line, err = buf.ReadString('\n')
	if err != nil {
		return fmt.Errorf("readstring: %w", err)
	}
	writing, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
	if err != nil {
		return fmt.Errorf("parseuint (%s): %w", strings.TrimSpace(line), err)
	}
	tags := r.getTags(addr)
	fields := map[string]interface{}{
		"calling": calling,
		"writing": writing,
	}
	acc.AddFields("raindrops", fields, tags)

	iterate := true
	var queuedLineStr string
	var activeLineStr string
	var activeErr error
	var queuedErr error

	for iterate {
		// Listen
		var tags map[string]string

		lis := map[string]interface{}{
			"active": 0,
			"queued": 0,
		}
		activeLineStr, activeErr = buf.ReadString('\n')
		if activeErr != nil {
			// iterate = false
			break
		}
		if strings.Compare(activeLineStr, "\n") == 0 {
			break
		}
		queuedLineStr, queuedErr = buf.ReadString('\n')
		if queuedErr != nil {
			iterate = false
		}
		activeLine := strings.Split(activeLineStr, " ")
		listenName := activeLine[0]

		active, err := strconv.ParseUint(strings.TrimSpace(activeLine[2]), 10, 64)
		if err != nil {
			active = 0
		}
		lis["active"] = active

		queuedLine := strings.Split(queuedLineStr, " ")
		queued, err := strconv.ParseUint(strings.TrimSpace(queuedLine[2]), 10, 64)
		if err != nil {
			queued = 0
		}
		lis["queued"] = queued
		if strings.Contains(listenName, ":") {
			listener := strings.Split(listenName, ":")
			tags = map[string]string{
				"ip":   listener[0],
				"port": listener[1],
			}

		} else {
			tags = map[string]string{
				"socket": listenName,
			}
		}
		acc.AddFields("raindrops_listen", lis, tags)
	}
	return nil
}

// Get tag(s) for the raindrops calling/writing plugin
func (r *Raindrops) getTags(addr *url.URL) map[string]string {
	h := addr.Host
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		host = addr.Host
		switch addr.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = ""
		}
	}
	return map[string]string{"server": host, "port": port}
}

func init() {
	inputs.Add("raindrops", func() cua.Input {
		return &Raindrops{httpClient: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 3 * time.Second,
			},
			Timeout: 4 * time.Second,
		}}
	})
}
