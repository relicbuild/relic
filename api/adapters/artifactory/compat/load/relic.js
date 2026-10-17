// k6 load test for Relic. Driven by run.sh.
//
// Scenarios, all at once against one local repository:
//   upload   - new files of 1 KiB..256 KiB with an X-Checksum-Sha256
//   churn    - overwrite and delete a small set of paths (makes garbage
//              for GC to collect while the test runs)
//   download - fetch seeded files and check their sha256
//   search   - AQL over the repository
import http from 'k6/http'
import { check } from 'k6'
import crypto from 'k6/crypto'
import encoding from 'k6/encoding'

const BASE = __ENV.RELIC_URL || 'http://localhost:28091'
const AUTH = { Authorization: 'Basic ' + encoding.b64encode('admin:' + (__ENV.RELIC_PASSWORD || 'password')) }
const REPO = 'load-local'
const DURATION = __ENV.DURATION || '2m'
const SEEDED = 50

export const options = {
  scenarios: {
    upload: { executor: 'constant-vus', vus: 8, duration: DURATION, exec: 'upload' },
    churn: { executor: 'constant-vus', vus: 4, duration: DURATION, exec: 'churn' },
    download: { executor: 'constant-vus', vus: 16, duration: DURATION, exec: 'download' },
    search: { executor: 'constant-arrival-rate', rate: 5, timeUnit: '1s', duration: DURATION, preAllocatedVUs: 4, exec: 'search' },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    'http_req_duration{op:upload}': ['p(95)<2000'],
    'http_req_duration{op:download}': ['p(95)<500'],
    'http_req_duration{op:search}': ['p(95)<1000'],
    checks: ['rate>0.999'],
  },
}

function payload(size, seed) {
  const prefix = `${seed}:${Date.now()}:${Math.random()}\n`
  return prefix + 'x'.repeat(Math.max(0, size - prefix.length))
}

function put(path, body, op) {
  return http.put(`${BASE}/artifactory/${REPO}/${path}`, body, {
    headers: Object.assign({ 'X-Checksum-Sha256': crypto.sha256(body, 'hex') }, AUTH),
    tags: { op },
  })
}

export function setup() {
  http.put(`${BASE}/artifactory/api/repositories/${REPO}`, JSON.stringify({ rclass: 'local', packageType: 'generic' }), {
    headers: Object.assign({ 'Content-Type': 'application/json' }, AUTH),
  })
  const seeded = []
  for (let i = 0; i < SEEDED; i++) {
    const body = payload(1024 + i * 1024, `seed${i}`)
    const res = put(`seed/${i}.bin`, body, 'seed')
    check(res, { 'seed stored': (r) => r.status === 201 })
    seeded.push({ path: `seed/${i}.bin`, sha: crypto.sha256(body, 'hex') })
  }
  return { seeded }
}

export function upload() {
  const size = 1024 + Math.floor(Math.random() * 255 * 1024)
  const res = put(`u/${__VU}/${__ITER}.bin`, payload(size, `u${__VU}`), 'upload')
  check(res, { 'upload 201': (r) => r.status === 201 })
}

export function churn() {
  const path = `churn/${__VU}/${__ITER % 5}.bin`
  if (__ITER % 3 === 2) {
    const res = http.del(`${BASE}/artifactory/${REPO}/${path}`, null, { headers: AUTH, tags: { op: 'delete' }, responseCallback: http.expectedStatuses(204, 404) })
    check(res, { 'delete 204/404': (r) => r.status === 204 || r.status === 404 })
    return
  }
  const res = put(path, payload(8 * 1024, `c${__VU}`), 'overwrite')
  check(res, { 'overwrite 201': (r) => r.status === 201 })
}

export function download(data) {
  const f = data.seeded[Math.floor(Math.random() * data.seeded.length)]
  const res = http.get(`${BASE}/artifactory/${REPO}/${f.path}`, { headers: AUTH, tags: { op: 'download' }, responseType: 'binary' })
  check(res, {
    'download 200': (r) => r.status === 200,
    'download sha256': (r) => crypto.sha256(r.body, 'hex') === f.sha,
  })
}

export function search() {
  const res = http.post(`${BASE}/artifactory/api/search/aql`, `items.find({"repo":"${REPO}","path":{"$match":"seed*"}}).include("name","sha256")`, {
    headers: Object.assign({ 'Content-Type': 'text/plain' }, AUTH),
    tags: { op: 'search' },
  })
  check(res, { 'search 200': (r) => r.status === 200 && JSON.parse(r.body).results.length === SEEDED })
}
