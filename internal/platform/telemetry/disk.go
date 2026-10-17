package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	disksMu sync.Mutex
	disks   = map[any]disk{}

	diskLog atomic.Pointer[slog.Logger]

	// PromQL: relic_disk_free_bytes / relic_disk_size_bytes < 0.1   (alert RelicDiskSpaceLow)
	diskFree = gaugeDesc(
		"disk_free_bytes",
		"Free space on the volume Relic keeps data on (SQLite, the filesystem object store).",
		"volume",
	)
	diskSize = gaugeDesc(
		"disk_size_bytes",
		"Size of the volume Relic keeps data on (SQLite, the filesystem object store).",
		"volume",
	)
	// PromQL: relic_db_sqlite_file_bytes
	sqliteFile = gaugeDesc("db_sqlite_file_bytes", "Size of the SQLite database, its write-ahead log included.")
)

// Volume names, the values of the volume label.
const (
	VolumeDatabase      = "database"
	VolumeObjectStorage = "object_storage"
)

type disk struct {
	volume string
	usage  func() (free, size uint64, err error)
	file   func() (int64, error)
}

func gaugeDesc(name, help string, labels ...string) *prometheus.Desc {
	describe(Metric{
		Name:   namespace + "_" + name,
		Type:   "gauge",
		Help:   help,
		Labels: labels,
	})

	return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
}

// RegisterDisk adds a volume to relic_disk_free_bytes and
// relic_disk_size_bytes until the returned function runs.
func RegisterDisk(volume string, usage func() (free, size uint64, err error), file func() (int64, error)) func() {
	key := new(int)

	disksMu.Lock()
	defer disksMu.Unlock()

	disks[key] = disk{
		volume: volume,
		usage:  usage,
		file:   file,
	}

	return func() {
		disksMu.Lock()
		defer disksMu.Unlock()

		delete(disks, key)
	}
}

type diskCollector struct{}

func (diskCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		diskFree,
		diskSize,
		sqliteFile,
	} {
		ch <- d
	}
}

func (diskCollector) Collect(ch chan<- prometheus.Metric) {
	disksMu.Lock()

	list := make([]disk, 0, len(disks))
	for _, d := range disks {
		list = append(list, d)
	}
	disksMu.Unlock()

	free := map[string]float64{}
	size := map[string]float64{}

	var file float64

	files := 0

	for _, d := range list {
		if f, s, err := d.usage(); err == nil {
			free[d.volume] += float64(f)
			size[d.volume] += float64(s)
		} else {
			logDiskError("read volume usage", d.volume, err)
		}

		if d.file != nil {
			if n, err := d.file(); err == nil {
				file += float64(n)
				files++
			} else {
				logDiskError("read database file size", d.volume, err)
			}
		}
	}

	for volume := range size {
		for _, m := range []prometheus.Metric{
			prometheus.MustNewConstMetric(diskFree, prometheus.GaugeValue, free[volume], volume),
			prometheus.MustNewConstMetric(diskSize, prometheus.GaugeValue, size[volume], volume),
		} {
			ch <- m
		}
	}

	if files > 0 {
		ch <- prometheus.MustNewConstMetric(sqliteFile, prometheus.GaugeValue, file)
	}
}

func logDiskError(msg, volume string, err error) {
	log := diskLog.Load()
	if log == nil {
		log = slog.Default()
	}

	log.WarnContext(context.Background(), msg, "volume", volume, "error", err)
}

func init() { registry.MustRegister(diskCollector{}) }
