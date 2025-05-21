package main

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ChannelIdNameMap represents the XML structure for channel IDs and names.
type ChannelIdNameMap struct {
	XMLName xml.Name       `xml:"map"`
	Entries []ChannelEntry `xml:"entry"`
}

// ChannelEntry represents a single entry in the ChannelIdNameMap.
type ChannelEntry struct {
	XMLName xml.Name `xml:"entry"`
	Values  []string `xml:"string"`
}

// ChannelStatsList represents the XML structure for channel statistics.
type ChannelStatsList struct {
	XMLName  xml.Name       `xml:"list"`
	Channels []ChannelStats `xml:"channelStatistics"`
}

// ChannelStats represents a single channel's statistics.
type ChannelStats struct {
	XMLName   xml.Name `xml:"channelStatistics"`
	ServerId  string   `xml:"serverId"`
	ChannelId string   `xml:"channelId"`
	Received  string   `xml:"received"`
	Sent      string   `xml:"sent"`
	Error     string   `xml:"error"`
	Filtered  string   `xml:"filtered"`
	Queued    string   `xml:"queued"`
}

const (
	namespace        = "mirth"
	channelIdNameAPI = "/channels/idsAndNames"
	channelStatsAPI  = "/channels/statistics"
	// DefaultHTTPClientTimeout is the default timeout for HTTP client requests.
	DefaultHTTPClientTimeout = 10 * time.Second
)

var (
	// httpClient is a shared HTTP client with insecure TLS verification for Mirth.
	httpClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: DefaultHTTPClientTimeout,
	}

	// Command-line flags
	listenAddress = flag.String("web.listen-address", ":9141", "Address to listen on for telemetry")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics")
	configPath    = flag.String("config.file-path", "", "Path to environment file")

	// Prometheus Metrics
	up = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last Mirth query successful.",
		nil, nil,
	)
	messagesReceived = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "messages_received_total"),
		"How many messages have been received (per channel).",
		[]string{"channel"}, nil,
	)
	messagesFiltered = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "messages_filtered_total"),
		"How many messages have been filtered (per channel).",
		[]string{"channel"}, nil,
	)
	messagesQueued = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "messages_queued"),
		"How many messages are currently queued (per channel).",
		[]string{"channel"}, nil,
	)
	messagesSent = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "messages_sent_total"),
		"How many messages have been sent (per channel).",
		[]string{"channel"}, nil,
	)
	messagesErrored = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "messages_errored_total"),
		"How many messages have errored (per channel).",
		[]string{"channel"}, nil,
	)
)

// Exporter collects Mirth statistics and exports them as Prometheus metrics.
type Exporter struct {
	mirthEndpoint string
	mirthUsername string
	mirthPassword string
}

// NewExporter creates a new Exporter instance.
func NewExporter(mirthEndpoint, mirthUsername, mirthPassword string) *Exporter {
	return &Exporter{
		mirthEndpoint: mirthEndpoint,
		mirthUsername: mirthUsername,
		mirthPassword: mirthPassword,
	}
}

// Describe sends the metric descriptions to the provided channel.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- messagesReceived
	ch <- messagesFiltered
	ch <- messagesQueued
	ch <- messagesSent
	ch <- messagesErrored
}

// Collect fetches the Mirth statistics and delivers them as Prometheus metrics.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	channelIDNameMap, err := e.loadChannelIDNameMap()
	if err != nil {
		log.Printf("ERROR: Failed to load channel ID to name map: %v", err)
		ch <- prometheus.MustNewConstMetric(up, prometheus.GaugeValue, 0)
		return
	}

	ch <- prometheus.MustNewConstMetric(up, prometheus.GaugeValue, 1) // Mirth API is accessible

	if err := e.hitMirthRestAPIsAndUpdateMetrics(channelIDNameMap, ch); err != nil {
		log.Printf("ERROR: Failed to collect channel statistics: %v", err)
		// Optionally set 'up' to 0 if subsequent collection fails after initial success
		// ch <- prometheus.MustNewConstMetric(up, prometheus.GaugeValue, 0)
	} else {
		log.Println("Successfully scraped Mirth endpoint.")
	}
}

// loadChannelIDNameMap fetches channel IDs and names from Mirth and returns them as a map.
func (e *Exporter) loadChannelIDNameMap() (map[string]string, error) {
	channelIDNameMap := make(map[string]string)

	req, err := http.NewRequestWithContext(context.Background(), "GET", e.mirthEndpoint+channelIdNameAPI, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for channel ID names: %w", err)
	}

	req.SetBasicAuth(e.mirthUsername, e.mirthPassword)
	req.Header.Set("X-Requested-With", "OpenAPI")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to perform HTTP request for channel ID names: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-OK status code %d from channel ID names API: %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body for channel ID names: %w", err)
	}

	var channelIDNameMapXML ChannelIdNameMap
	if err := xml.Unmarshal(body, &channelIDNameMapXML); err != nil {
		return nil, fmt.Errorf("failed to unmarshal XML for channel ID names: %w", err)
	}

	for _, entry := range channelIDNameMapXML.Entries {
		if len(entry.Values) == 2 {
			channelIDNameMap[entry.Values[0]] = entry.Values[1]
		} else {
			log.Printf("WARNING: Unexpected number of values (%d) in channel ID name entry: %v", len(entry.Values), entry.Values)
		}
	}

	return channelIDNameMap, nil
}

// hitMirthRestAPIsAndUpdateMetrics fetches channel statistics and updates Prometheus metrics.
func (e *Exporter) hitMirthRestAPIsAndUpdateMetrics(channelIDNameMap map[string]string, ch chan<- prometheus.Metric) error {
	req, err := http.NewRequestWithContext(context.Background(), "GET", e.mirthEndpoint+channelStatsAPI, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for channel statistics: %w", err)
	}

	req.SetBasicAuth(e.mirthUsername, e.mirthPassword)
	req.Header.Set("X-Requested-With", "OpenAPI")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to perform HTTP request for channel statistics: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-OK status code %d from channel statistics API: %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body for channel statistics: %w", err)
	}

	var channelStatsList ChannelStatsList
	if err := xml.Unmarshal(body, &channelStatsList); err != nil {
		return fmt.Errorf("failed to unmarshal XML for channel statistics: %w", err)
	}

	for _, channel := range channelStatsList.Channels {
		channelName, ok := channelIDNameMap[channel.ChannelId]
		if !ok {
			log.Printf("WARNING: Channel ID '%s' not found in ID-name map. Skipping metrics for this channel.", channel.ChannelId)
			continue
		}

		// Helper function to parse and send metric
		sendMetric := func(desc *prometheus.Desc, valueStr string) {
			value, err := strconv.ParseFloat(valueStr, 64)
			if err != nil {
				log.Printf("WARNING: Failed to parse metric value '%s' for channel '%s': %v", valueStr, channelName, err)
				return
			}
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, channelName)
		}

		sendMetric(messagesReceived, channel.Received)
		sendMetric(messagesSent, channel.Sent)
		sendMetric(messagesErrored, channel.Error)
		sendMetric(messagesFiltered, channel.Filtered)
		sendMetric(messagesQueued, channel.Queued)
	}

	return nil
}

func main() {
	flag.Parse()

	// Load environment variables
	if *configPath != "" {
		log.Printf("Loading environment file from: %s", *configPath)
		if err := godotenv.Load(*configPath); err != nil {
			log.Fatalf("FATAL: Error loading environment file %s: %v", *configPath, err)
		}
	} else {
		if err := godotenv.Load(); err != nil {
			log.Println("WARNING: Error loading .env file, assuming environment variables are set or not needed.")
		}
	}

	mirthEndpoint := os.Getenv("MIRTH_BASE_API_URL")
	mirthUsername := os.Getenv("MIRTH_USERNAME")
	mirthPassword := os.Getenv("MIRTH_PASSWORD")

	if mirthEndpoint == "" {
		log.Fatal("FATAL: MIRTH_BASE_API_URL environment variable is not set.")
	}
	if mirthUsername == "" || mirthPassword == "" {
		log.Fatal("FATAL: MIRTH_USERNAME or MIRTH_PASSWORD environment variables are not set. Basic authentication is required.")
	}

	exporter := NewExporter(mirthEndpoint, mirthUsername, mirthPassword)
	prometheus.MustRegister(exporter)
	log.Printf("Mirth Exporter started. Using Mirth endpoint: %s", mirthEndpoint)
	log.Printf("Metrics exposed on %s%s", *listenAddress, *metricsPath)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`<html>
             <head><title>Mirth Channel Exporter</title></head>
             <body>
             <h1>Mirth Channel Exporter</h1>
             <p><a href='%s'>Metrics</a></p>
             </body>
             </html>`, *metricsPath)))
	})

	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}