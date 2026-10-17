-- name: RecordPolicyDecision :exec
INSERT INTO policy_decisions (kind, subject, policy, watch, rule, reason, actor, count, first_seen, last_seen)
VALUES (@kind, @subject, @policy, @watch, @rule, @reason, @actor, 1, @now, @now)
ON CONFLICT (kind, subject, policy, watch, rule) DO UPDATE SET reason = excluded.reason, actor = excluded.actor,
    count = policy_decisions.count + 1, last_seen = excluded.last_seen;

-- name: ListPolicyDecisions :many
SELECT * FROM policy_decisions
WHERE (@kind = '' OR kind = @kind)
ORDER BY last_seen DESC, kind, subject
LIMIT @max_rows;

-- name: ListScannedContents :many
SELECT a.sha256, a.sbom, i.repo_key, i.path, i.name
FROM scan_artifacts a
JOIN items i ON i.id = (SELECT id FROM items
                        WHERE sha256 = a.sha256 AND type = 'file'
                          AND (json_array_length(@repositories) = 0
                               OR repo_key IN (SELECT value FROM json_each(@repositories)))
                        ORDER BY repo_key, path, name LIMIT 1)
WHERE a.status IN ('done', 'partial') AND a.sha256 > @after
ORDER BY a.sha256
LIMIT @max_rows;
