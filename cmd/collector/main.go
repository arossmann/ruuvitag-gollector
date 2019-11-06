package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/niktheblak/gcloudzap"
	"github.com/niktheblak/ruuvitag-gollector/pkg/exporter"
	"github.com/niktheblak/ruuvitag-gollector/pkg/exporter/console"
	"github.com/niktheblak/ruuvitag-gollector/pkg/exporter/influxdb"
	"github.com/niktheblak/ruuvitag-gollector/pkg/exporter/pubsub"
	"github.com/niktheblak/ruuvitag-gollector/pkg/scanner"
	"github.com/urfave/cli"
	"github.com/urfave/cli/altsrc"
	"go.uber.org/zap"
)

var logger *zap.Logger

func run(c *cli.Context) error {
	if c.GlobalIsSet("application_credentials") {
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", c.GlobalString("application_credentials"))
	}
	if c.GlobalBool("stackdriver") {
		project := c.GlobalString("project")
		if project == "" {
			return fmt.Errorf("Google Cloud Platform project must be specified")
		}
		var err error
		logger, err = gcloudzap.NewProduction(project, "ruuvitag-gollector")
		if err != nil {
			return fmt.Errorf("failed to create Stackdriver logger: %w", err)
		}
	} else {
		var err error
		logger, err = zap.NewDevelopment()
		if err != nil {
			return fmt.Errorf("failed to create logger: %w", err)
		}
	}
	defer logger.Sync()
	ruuviTags, err := parseRuuviTags(c.GlobalStringSlice("ruuvitags"))
	if err != nil {
		return fmt.Errorf("failed to parse RuuviTag addresses: %w", err)
	}
	scn := scanner.New(logger, ruuviTags)
	defer scn.Close()
	var exporters []exporter.Exporter
	if c.GlobalBool("console") {
		exporters = append(exporters, console.Exporter{})
	}
	if c.GlobalBool("influxdb") {
		url := c.GlobalString("influxdb_addr")
		if url == "" {
			return fmt.Errorf("InfluxDB address must be specified")
		}
		influx, err := influxdb.New(influxdb.Config{
			Addr:        url,
			Database:    c.GlobalString("influxdb_database"),
			Measurement: c.GlobalString("influxdb_measurement"),
			Username:    c.GlobalString("influxdb_username"),
			Password:    c.GlobalString("influxdb_password"),
		})
		if err != nil {
			return fmt.Errorf("failed to create InfluxDB reporter: %w", err)
		}
		exporters = append(exporters, influx)
	}
	if c.GlobalBool("pubsub") {
		ctx := context.Background()
		project := c.GlobalString("project")
		if project == "" {
			return fmt.Errorf("Google Cloud Platform project must be specified")
		}
		topic := c.GlobalString("pubsub_topic")
		if topic == "" {
			return fmt.Errorf("Google Pub/Sub topic must be specified")
		}
		ps, err := pubsub.New(ctx, project, topic)
		if err != nil {
			return fmt.Errorf("failed to create Google Pub/Sub reporter: %w", err)
		}
		exporters = append(exporters, ps)
	}
	scn.Exporters = exporters
	device := c.GlobalString("device")
	logger.Info("Initializing new device", zap.String("device", device))
	if err := scn.Init(device); err != nil {
		return err
	}
	logger.Info("Starting ruuvitag-gollector")
	if c.GlobalBool("daemon") {
		runAsDaemon(scn, c.GlobalDuration("scan_interval"))
		return nil
	} else {
		return runOnce(scn)
	}
}

func runAsDaemon(scn *scanner.Scanner, scanInterval time.Duration) {
	logger.Info("Starting scanner")
	ctx := context.Background()
	if scanInterval > 0 {
		scn.ScanWithInterval(ctx, scanInterval)
	} else {
		scn.ScanContinuously(ctx)
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	select {
	case <-interrupt:
	case <-scn.Quit:
	}
	logger.Info("Stopping ruuvitag-gollector")
	scn.Stop()
}

func runOnce(scn *scanner.Scanner) error {
	logger.Info("Scanning once")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	go func() {
		<-interrupt
		cancel()
		scn.Stop()
	}()
	if err := scn.ScanOnce(ctx); err != nil {
		return fmt.Errorf("failed to scan: %w", err)
	}
	logger.Info("Stopping ruuvitag-gollector")
	return nil
}

func parseRuuviTags(ruuviTags []string) (map[string]string, error) {
	m := make(map[string]string)
	for _, rt := range ruuviTags {
		tokens := strings.SplitN(rt, "=", 2)
		if len(tokens) != 2 {
			return nil, fmt.Errorf("invalid RuuviTag entry: %s", rt)
		}
		addr := strings.ToLower(strings.TrimSpace(tokens[0]))
		name := strings.TrimSpace(tokens[1])
		m[addr] = name
	}
	return m, nil
}

func main() {
	app := cli.NewApp()
	app.Name = "ruuvitag-gollector"
	app.Usage = "Collect measurements from RuuviTag sensors"
	app.Version = "1.0.0"
	app.Author = "Niko Korhonen"
	app.Email = "niko@bitnik.fi"
	app.Copyright = "(c) 2019 Niko Korhonen"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:      "config",
			Usage:     "RuuviTag configuration file",
			EnvVar:    "RUUVITAG_CONFIG_FILE",
			TakesFile: true,
			Required:  true,
		},
		cli.BoolFlag{
			Name:  "daemon, d",
			Usage: "run as a background service",
		},
		cli.BoolFlag{
			Name:  "console, c",
			Usage: "print measurements to console",
		},
		altsrc.NewStringSliceFlag(cli.StringSliceFlag{
			Name:  "ruuvitags",
			Usage: "RuuviTag addresses and names to use",
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "device",
			Usage:  "HCL device to use",
			EnvVar: "RUUVITAG_DEVICE",
			Value:  "default",
		}),
		cli.DurationFlag{
			Name:   "scan_interval",
			Usage:  "Pause between RuuviTag device scans in daemon mode",
			EnvVar: "RUUVITAG_SCAN_INTERVAL",
			Value:  1 * time.Minute,
		},
		cli.BoolFlag{
			Name:   "influxdb",
			Usage:  "use influxdb",
			EnvVar: "RUUVITAG_USE_INFLUXDB",
		},
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "influxdb_addr",
			Usage:  "InfluxDB server address",
			EnvVar: "RUUVITAG_INFLUXDB_ADDR",
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "influxdb_database",
			Usage:  "InfluxDB database",
			EnvVar: "RUUVITAG_INFLUXDB_DATABASE",
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "influxdb_measurement",
			Usage:  "InfluxDB measurement",
			EnvVar: "RUUVITAG_INFLUXDB_MEASUREMENT",
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "influxdb_username",
			Usage:  "InfluxDB username",
			EnvVar: "RUUVITAG_INFLUXDB_USERNAME",
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "influxdb_password",
			Usage:  "InfluxDB password",
			EnvVar: "RUUVITAG_INFLUXDB_PASSWORD",
		}),
		cli.BoolFlag{
			Name:   "pubsub",
			Usage:  "use Google Pub/Sub",
			EnvVar: "RUUVITAG_USE_PUBSUB",
		},
		altsrc.NewStringFlag(cli.StringFlag{
			Name: "application_credentials",
			Usage: "Google Cloud application credentials file",
			EnvVar: "RUUVITAG_APPLICATION_CREDENTIALS_FILE",
			TakesFile: true,
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "project",
			Usage:  "Google Cloud Platform project",
			EnvVar: "RUUVITAG_GOOGLE_PROJECT",
		}),
		altsrc.NewStringFlag(cli.StringFlag{
			Name:   "pubsub_topic",
			Usage:  "Google Pub/Sub topic",
			EnvVar: "RUUVITAG_PUBSUB_TOPIC",
		}),
		cli.BoolFlag{
			Name:   "stackdriver",
			Usage:  "use Google Stackdriver logging",
			EnvVar: "RUUVITAG_USE_STACKDRIVER_LOGGING",
		},
	}
	app.Before = altsrc.InitInputSourceWithContext(app.Flags, altsrc.NewTomlSourceFromFlagFunc("config"))
	app.Action = func(c *cli.Context) error {
		return run(c)
	}
	if err := app.Run(os.Args); err != nil {
		if logger != nil {
			logger.Error("Error while running application", zap.Error(err))
		} else {
			log.Fatalf("Error while running application: %v", err)
		}
	}
}
