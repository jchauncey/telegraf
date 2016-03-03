package prometheus

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type Prometheus struct {
	Urls []string

	// Use SSL but skip chain & host verification
	InsecureSkipVerify bool
	// Bearer Token authorization file path
	BearerToken string `toml:"bearer_token"`
}

var sampleConfig = `
  ## An array of urls to scrape metrics from.
  urls = ["http://localhost:9100/metrics"]

	### Use SSL but skip chain & host verification
	# insecure_skip_verify = false
	### Use bearer token for authorization
	# bearer_token = /path/to/bearer/token
`

func (p *Prometheus) SampleConfig() string {
	return sampleConfig
}

func (p *Prometheus) Description() string {
	return "Read metrics from one or many prometheus clients"
}

var ErrProtocolError = errors.New("prometheus protocol error")

// Reads stats from all configured servers accumulates stats.
// Returns one of the errors encountered while gather stats (if any).
func (p *Prometheus) Gather(acc telegraf.Accumulator) error {
	var wg sync.WaitGroup

	var outerr error

	for _, serv := range p.Urls {
		wg.Add(1)
		go func(serv string) {
			defer wg.Done()
			outerr = p.gatherURL(serv, acc)
		}(serv)
	}

	wg.Wait()

	return outerr
}

func (p *Prometheus) gatherURL(url string, acc telegraf.Accumulator) error {
	var req, err = http.NewRequest("GET", url, nil)
	req.Header = make(http.Header)
	var token []byte
	var resp *http.Response

	var rt http.RoundTripper = &http.Transport{
		Dial: (&net.Dialer{
			Timeout:   4 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		ResponseHeaderTimeout: time.Duration(3 * time.Second),
	}

	if p.BearerToken != "" {
		token, err = ioutil.ReadFile(p.BearerToken)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+string(token))
	}

	resp, err = rt.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("error making HTTP request to %s: %s", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP status %s", url, resp.Status)
	}
	format := expfmt.ResponseFormat(resp.Header)
	decoder := expfmt.NewDecoder(resp.Body, format)
	options := &expfmt.DecodeOptions{
		Timestamp: model.Now(),
	}
	sampleDecoder := &expfmt.SampleDecoder{
		Dec:  decoder,
		Opts: options,
	}

	for {
		var samples model.Vector
		err := sampleDecoder.Decode(&samples)
		if err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("error getting processing samples for %s: %s", url, err)
		}
		for _, sample := range samples {
			tags := make(map[string]string)
			for key, value := range sample.Metric {
				if key == model.MetricNameLabel {
					continue
				}
				tags[string(key)] = string(value)
			}
			if sample.Value.String() != "NaN" {
				acc.Add("prometheus_"+string(sample.Metric[model.MetricNameLabel]), float64(sample.Value), tags)
			} else {
				acc.Add("prometheus_"+string(sample.Metric[model.MetricNameLabel]), float64(0), tags)
			}
		}
	}

	return nil
}

func init() {
	inputs.Add("prometheus", func() telegraf.Input {
		return &Prometheus{}
	})
}
