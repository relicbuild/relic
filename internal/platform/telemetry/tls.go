package telemetry

import (
	"slices"
	"time"
)

var (
	// PromQL: min by (instance, source) (relic_tls_certificate_expiry_timestamp_seconds) - time() < 14 * 86400   (alert RelicServerCertificateExpiring)
	tlsCertificateExpiry = gaugeVec(
		"tls_certificate_expiry_timestamp_seconds",
		"When the certificate the server serves expires, in Unix seconds, by source (operator, generated).",
		"source",
	)

	// PromQL: sum by (result) (increase(relic_tls_certificates_issued_total[1d]))
	tlsCertificatesIssued = counterVec(
		"tls_certificates_issued_total",
		"Server certificates issued by the certificate authority Relic generated, by result.",
		"result",
	)
)

// Sources of the certificate the server serves, the values of the source
// label.
const (
	TLSSourceOperator  = "operator"
	TLSSourceGenerated = "generated"
)

// TLSCertificateServed records when the certificate the server serves
// expires.
func TLSCertificateServed(source string, notAfter time.Time) {
	if !slices.Contains([]string{
		TLSSourceOperator,
		TLSSourceGenerated,
	}, source) {
		source = Other
	}

	tlsCertificateExpiry.WithLabelValues(source).Set(float64(notAfter.Unix()))
}

// TLSCertificateIssued counts a server certificate the generated authority
// issued.
func TLSCertificateIssued(err error) {
	tlsCertificatesIssued.WithLabelValues(Result(err)).Inc()
}
