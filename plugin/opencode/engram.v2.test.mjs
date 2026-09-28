import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { createRequire, syncBuiltinESMExports } from "node:module"
import { test } from "node:test"

const require = createRequire(import.meta.url)
const childProcess = require("node:child_process")
const fs = require("node:fs")

const source = readFileSync(new URL("./engram.ts", import.meta.url), "utf8")

const DIRECTORY = "/work/engram"
const PROJECT_ID = "project-1"
const INSTANCE_ID = "00000000000000000000000000000000"
let runtimeImport = 0

function httpResponse(data) {
  return { ok: true, async json() { return data } }
}

// Minimal OpenCode V2 event stream. emit() resolves once the plugin asks for
// the next event, which proves the previous one was fully handled. end() and
// fail() interrupt only the current subscription, like a server restart.
function eventStream() {
  const queued = []
  let waiting
  let waitingReject
  let pulled
  let closed = false
  let interrupt
  const subscriptions = []
  const settle = (result) => {
    const resolve = waiting
    const reject = waitingReject
    waiting = undefined
    waitingReject = undefined
    if (result instanceof Error) reject(result)
    else resolve(result)
  }
  return {
    subscriptions,
    subscribe(options) {
      subscriptions.push(options)
      options?.signal?.addEventListener("abort", () => {
        closed = true
        if (waiting) settle({ value: undefined, done: true })
      })
      return {
        [Symbol.asyncIterator]() {
          return {
            next() {
              pulled?.()
              pulled = undefined
              if (interrupt) {
                const result = interrupt
                interrupt = undefined
                return result instanceof Error ? Promise.reject(result) : Promise.resolve(result)
              }
              if (queued.length > 0) return Promise.resolve({ value: queued.shift(), done: false })
              if (closed) return Promise.resolve({ value: undefined, done: true })
              return new Promise((resolve, reject) => {
                waiting = resolve
                waitingReject = reject
              })
            },
            async return() {
              closed = true
              return { value: undefined, done: true }
            },
          }
        },
      }
    },
    emit(event) {
      const handled = new Promise((resolve) => { pulled = resolve })
      if (waiting) settle({ value: event, done: false })
      else queued.push(event)
      return handled
    },
    end() {
      const result = { value: undefined, done: true }
      if (waiting) settle(result)
      else interrupt = result
    },
    fail(error) {
      if (waiting) settle(error)
      else interrupt = error
    },
    closeForever() {
      closed = true
      if (waiting) settle({ value: undefined, done: true })
    },
  }
}

function withTimeout(promise, message, ms = 1000) {
  let timer
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(message)), ms) })
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer))
}

async function waitFor(condition, message, ms = 1000) {
  const deadline = Date.now() + ms
  while (!condition()) {
    if (Date.now() > deadline) throw new Error(message)
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

async function setupV2(t, { sessions = new Map() } = {}) {
  const originalFetch = globalThis.fetch
  const originalBun = globalThis.Bun
  const originalEngramURL = process.env.ENGRAM_URL
  const originalSpawnSync = childProcess.spawnSync
  const originalSpawn = childProcess.spawn
  const originalExistsSync = fs.existsSync
  delete globalThis.Bun
  delete process.env.ENGRAM_URL
  childProcess.spawnSync = () => ({ status: 0, stdout: `${INSTANCE_ID}\n` })
  childProcess.spawn = () => ({ on() { return this }, unref() {} })
  fs.existsSync = () => false
  syncBuiltinESMExports()

  const requests = []
  globalThis.fetch = async (url, init) => {
    const path = new URL(url).pathname
    if (path === "/health") return httpResponse({ status: "ok", instance_id: INSTANCE_ID })
    const body = init?.body ? JSON.parse(init.body) : undefined
    requests.push({ path, method: init?.method, body })
    if (path === "/project/current") return httpResponse({ project: "engram", project_source: "git_remote" })
    if (path === "/sessions") return httpResponse({ id: body.id, status: "created" })
    if (path === "/context/compaction") return httpResponse({ context: "previous session context" })
    return httpResponse({})
  }

  t.after(() => {
    globalThis.fetch = originalFetch
    globalThis.Bun = originalBun
    if (originalEngramURL === undefined) delete process.env.ENGRAM_URL
    else process.env.ENGRAM_URL = originalEngramURL
    childProcess.spawnSync = originalSpawnSync
    childProcess.spawn = originalSpawn
    fs.existsSync = originalExistsSync
    syncBuiltinESMExports()
  })

  const hooks = new Map()
  const disposedHooks = []
  const sessionGetIDs = []
  const register = (domain) => async (name, callback) => {
    hooks.set(`${domain}.${name}`, callback)
    return { dispose: async () => { disposedHooks.push(`${domain}.${name}`) } }
  }
  const events = eventStream()
  const ctx = {
    location: { directory: DIRECTORY, project: { id: PROJECT_ID, directory: DIRECTORY, canonical: DIRECTORY } },
    event: { subscribe: (options) => events.subscribe(options) },
    session: {
      hook: register("session"),
      async get({ sessionID }) {
        sessionGetIDs.push(sessionID)
        const info = sessions.get(sessionID)
        if (!info) throw new Error(`session ${sessionID} not found`)
        return info
      },
    },
    tool: { hook: register("tool") },
  }

  runtimeImport += 1
  const module = await import(new URL(`./engram.ts?v2-runtime=${runtimeImport}`, import.meta.url).href)
  const cleanup = await module.default.setup(ctx)
  return {
    module,
    cleanup,
    hooks,
    disposedHooks,
    requests,
    sessionGetIDs,
    events,
    created: (sessionID, parentID) => events.emit({
      type: "session.created",
      data: { sessionID, projectID: PROJECT_ID, location: { directory: DIRECTORY }, ...(parentID ? { parentID } : {}) },
    }),
    deleted: (sessionID) => events.emit({ type: "session.deleted", data: { sessionID } }),
    posts: (path) => requests.filter((request) => request.method === "POST" && request.path === path),
  }
}

function sessionInfo(id, parentID) {
  return { id, projectID: PROJECT_ID, ...(parentID ? { parentID } : {}) }
}

test("default export serves V1 through server and V2 through setup", async () => {
  const module = await import(new URL("./engram.ts?v2-shape", import.meta.url).href)
  assert.equal(module.default.id, "engram")
  assert.strictEqual(module.default.server, module.Engram)
  assert.equal(typeof module.default.setup, "function")
  assert.doesNotMatch(source, /^import\s+(?!type\b)[^\n]*from\s+"@opencode(-ai)?\/plugin"/m, "V1 hosts may lack the V2 SDK")
})

test("V2 setup registers session, tool, and event hooks and cleans them up", async (t) => {
  const runtime = await setupV2(t)
  assert.deepEqual([...runtime.hooks.keys()].sort(), [
    "session.compaction",
    "session.context",
    "session.prompt",
    "tool.execute.after",
    "tool.execute.before",
  ])
  assert.equal(runtime.events.subscriptions.length, 1)
  assert.equal(typeof runtime.cleanup, "function")

  await runtime.created("ses_root")
  await runtime.cleanup()

  assert.equal(runtime.disposedHooks.length, 5)
  assert.equal(runtime.events.subscriptions[0].signal.aborted, true)
  assert.equal(runtime.posts("/sessions/ses_root/end").length, 1, "cleanup ends registered sessions")
})

test("V2 session.created binds root sessions but never child sessions", async (t) => {
  const runtime = await setupV2(t)
  await runtime.created("ses_root")
  await runtime.created("ses_child", "ses_root")

  assert.deepEqual(runtime.posts("/sessions").map(({ body }) => body), [
    { id: "ses_root", project: "engram", directory: DIRECTORY },
  ])

  await runtime.deleted("ses_root")
  assert.equal(runtime.posts("/sessions/ses_root/end").length, 1)
})

test("V2 ignores other locations and leaves unrelated tool input untouched", async (t) => {
  const runtime = await setupV2(t)
  await runtime.events.emit({
    type: "session.created",
    data: { sessionID: "ses_elsewhere", projectID: PROJECT_ID, location: { directory: "/work/other" } },
  })
  assert.equal(runtime.posts("/sessions").length, 0)

  const call = { tool: "bash", sessionID: "ses_root", input: undefined }
  await runtime.hooks.get("tool.execute.before")(call)
  assert.equal(call.input, undefined)
})

test("V2 setup releases earlier registrations when a later one fails", async (t) => {
  const runtime = await setupV2(t)
  await runtime.cleanup()
  const ctx = {
    location: { directory: DIRECTORY, project: { id: PROJECT_ID } },
    event: { subscribe: () => { throw new Error("unexpected subscribe") } },
    session: {
      get: async () => { throw new Error("unexpected get") },
      hook: async (name) => ({ dispose: async () => { disposed.push(name) } }),
    },
    tool: { hook: async () => { throw new Error("tool hooks unavailable") } },
  }
  const disposed = []
  await assert.rejects(runtime.module.default.setup(ctx), /tool hooks unavailable/)
  assert.deepEqual(disposed, ["prompt", "context", "compaction"])
})

test("V2 prompt hook captures user prompts for the authoritative session", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  await runtime.hooks.get("session.prompt")({
    sessionID: "ses_root",
    messageID: "msg_1",
    prompt: { text: "Please remember the <private>token</private> decision" },
    delivery: "immediate",
  })

  assert.deepEqual(runtime.posts("/prompts").map(({ body }) => body), [
    { session_id: "ses_root", content: "Please remember the [REDACTED] decision", project: "engram" },
  ])
})

test("V2 tool hooks bind Engram writes to the root session and capture subagent output", async (t) => {
  const runtime = await setupV2(t, {
    sessions: new Map([["ses_root", sessionInfo("ses_root")], ["ses_child", sessionInfo("ses_child", "ses_root")]]),
  })
  const call = { tool: "engram_mem_save", sessionID: "ses_child", agent: "build", messageID: "msg_1", id: "call_1", input: { title: "x" } }
  await runtime.hooks.get("tool.execute.before")(call)
  assert.equal(call.input.session_id, "ses_root")
  assert.deepEqual(runtime.sessionGetIDs, ["ses_child", "ses_root"])

  const output = "Subagent finished: the auth middleware now validates JWT expiry before routing."
  await runtime.hooks.get("tool.execute.after")({
    tool: "subagent",
    sessionID: "ses_root",
    agent: "build",
    messageID: "msg_2",
    id: "call_2",
    input: {},
    status: "completed",
    result: { content: [{ type: "text", text: output }] },
  })
  assert.deepEqual(runtime.posts("/observations/passive").map(({ body }) => body), [
    { session_id: "ses_root", content: output, project: "engram", source: "task-complete" },
  ])
})

test("V2 tool hook rejects Engram writes without an authoritative session", async (t) => {
  const runtime = await setupV2(t)
  const call = { tool: "engram_mem_save", sessionID: "ses_missing", input: {} }
  await assert.rejects(runtime.hooks.get("tool.execute.before")(call), /authoritative OpenCode runtime session/)
  assert.equal(call.input.session_id, undefined)
})

test("V2 context hook appends memory instructions to the last system part", async (t) => {
  const runtime = await setupV2(t)
  const request = { sessionID: "ses_root", system: [{ type: "text", text: "base" }], messages: [] }
  await runtime.hooks.get("session.context")(request)
  assert.equal(request.system.length, 1)
  assert.match(request.system[0].text, /^base\n\n## Engram Persistent Memory/)

  const empty = { sessionID: "ses_root", system: [], messages: [] }
  await runtime.hooks.get("session.context")(empty)
  assert.equal(empty.system.length, 1)
  assert.equal(empty.system[0].type, "text")
  assert.match(empty.system[0].text, /^## Engram Persistent Memory/)
})

test("V2 compaction hook injects session context and the summary instruction", async (t) => {
  const runtime = await setupV2(t, { sessions: new Map([["ses_root", sessionInfo("ses_root")]]) })
  const compaction = { sessionID: "ses_root", system: [{ type: "text", text: "summarize" }], messages: [] }
  await runtime.hooks.get("session.compaction")(compaction)

  assert.equal(compaction.system.length, 1)
  assert.match(compaction.system[0].text, /^summarize\n\nprevious session context\n\nCRITICAL INSTRUCTION FOR COMPACTED SUMMARY/)
  assert.match(compaction.system[0].text, /Use project: 'engram'/)
  assert.equal(runtime.requests.filter(({ path }) => path === "/context/compaction").length, 1)
})

test("V2 re-subscribes when the event stream ends", async (t) => {
  const runtime = await setupV2(t)
  runtime.events.end()
  await waitFor(() => runtime.events.subscriptions.length === 2, "plugin did not re-subscribe after the stream ended")

  await withTimeout(runtime.created("ses_root"), "event after re-subscription was not handled")
  assert.equal(runtime.posts("/sessions").length, 1)
  await runtime.cleanup()
})

test("V2 re-subscribes when the event stream throws", async (t) => {
  const runtime = await setupV2(t)
  runtime.events.fail(new Error("stream reset"))
  await waitFor(() => runtime.events.subscriptions.length === 2, "plugin did not re-subscribe after the stream failed")

  await withTimeout(runtime.created("ses_root"), "event after re-subscription was not handled")
  assert.equal(runtime.posts("/sessions").length, 1)
  await runtime.cleanup()
})

test("V2 keeps listening when handling one event throws", async (t) => {
  const runtime = await setupV2(t)
  const poisoned = { type: "session.created", get data() { throw new Error("malformed event") } }
  await withTimeout(runtime.events.emit(poisoned), "loop stopped pulling after a failing event")
  await withTimeout(runtime.created("ses_root"), "event after a failing event was not handled")

  assert.equal(runtime.posts("/sessions").length, 1)
  assert.equal(runtime.events.subscriptions.length, 1, "a failing event must not drop the subscription")
  await runtime.cleanup()
})

test("V2 backs off between re-subscriptions and stops after cleanup", async (t) => {
  const runtime = await setupV2(t)
  runtime.events.closeForever()
  await new Promise((resolve) => setTimeout(resolve, 300))
  const attempts = runtime.events.subscriptions.length
  assert.ok(attempts >= 2 && attempts <= 6, `expected bounded re-subscription attempts, got ${attempts}`)

  await withTimeout(runtime.cleanup(), "cleanup waited on the reconnect backoff")
  await new Promise((resolve) => setTimeout(resolve, 150))
  assert.equal(runtime.events.subscriptions.length, attempts, "no re-subscription after cleanup")
})
