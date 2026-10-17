-- name: GetQuarantineDecision :one
SELECT * FROM quarantine_decisions WHERE repo_key = @repo_key AND sha256 = @sha256;

-- name: PutQuarantineDecision :exec
INSERT INTO quarantine_decisions (repo_key, sha256, decision, reason, decided_by, decided)
VALUES (@repo_key, @sha256, @decision, @reason, @decided_by, @decided)
ON CONFLICT (repo_key, sha256) DO UPDATE
SET decision = EXCLUDED.decision, reason = EXCLUDED.reason, decided_by = EXCLUDED.decided_by, decided = EXCLUDED.decided;

-- name: ListQuarantineCandidates :many
-- Files of the given repositories that are held: rejected, not scanned yet,
-- failed to scan, skipped by the scan or malicious, with their scan state and
-- any rejection. Only files whose name matches the pattern, a
-- case-insensitive regular expression, are scanned and so can be held.
SELECT i.repo_key, i.path, i.name, i.sha256, i.created,
       coalesce(s.status, '')::text AS scan_status,
       EXISTS (SELECT 1 FROM scan_findings f WHERE f.sha256 = i.sha256 AND f.vuln_id LIKE 'MAL-%') AS malicious,
       coalesce(d.decision, '')::text AS decision,
       coalesce(d.reason, '')::text AS decision_reason,
       coalesce(d.decided_by, '')::text AS decided_by
FROM items i
LEFT JOIN scan_artifacts s ON s.sha256 = i.sha256
LEFT JOIN quarantine_decisions d ON d.repo_key = i.repo_key AND d.sha256 = i.sha256
WHERE i.repo_key = ANY(@repo_keys::text[]) AND i.type = 'file'
  AND i.name ~* @name_pattern::text
  AND (d.decision IS NULL OR d.decision = 'rejected')
  AND (d.decision = 'rejected' OR s.status IS NULL OR s.status IN ('queued', 'failed', 'skipped')
       OR EXISTS (SELECT 1 FROM scan_findings f WHERE f.sha256 = i.sha256 AND f.vuln_id LIKE 'MAL-%'))
ORDER BY i.created DESC, i.id
LIMIT @max_rows OFFSET @skip;
