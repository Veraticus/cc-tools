import assert from "node:assert/strict";
import { appendFileSync, existsSync } from "node:fs";
import { registerHooks } from "node:module";
import process from "node:process";

const marker = process.env.STEWARD_QUOTA_FIXTURE_MARKER;
if (marker === undefined) throw new Error("quota fixture marker is required");
const self = import.meta.url;
const accountID = "SYNTHETIC-QUOTA-ACCOUNT";
const credentialSentinel = `${Buffer.from("fixture-header").toString("base64url")}.${Buffer.from(JSON.stringify({
  "https://api.openai.com/auth": { chatgpt_account_id: accountID },
})).toString("base64url")}.SYNTHETIC-QUOTA-CREDENTIAL-SENTINEL`;
let getAuthCalls = 0;
let fetchCalls = 0;

/** @typedef {{signal: AbortSignal, allowModelNetwork: false}} CreateOptions */
/** @typedef {{provider: string, id: string, api: string, baseUrl: string}} FixtureModel */

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@earendil-works/pi-coding-agent") {
      return { url: self, shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

/** @param {string} path */
async function waitForFile(path) {
  const deadline = Date.now() + 5_000;
  while (!existsSync(path)) {
    if (Date.now() >= deadline) throw new Error("quota fixture release timed out");
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

export class ModelRuntime {
  /** @param {CreateOptions} options */
  static async create(options) {
    assert.equal(options.allowModelNetwork, false);
    assert.ok(options.signal instanceof AbortSignal);
    return new ModelRuntime();
  }

  /** @param {string} provider @param {string} id @returns {FixtureModel} */
  getModel(provider, id) {
    assert.equal(provider, "openai-codex");
    assert.equal(id, "gpt-5.6-luna");
    return {
      provider,
      id,
      api: "openai-codex-responses",
      baseUrl: "https://chatgpt.com/backend-api",
    };
  }

  /** @param {FixtureModel} model @param {{signal: AbortSignal}} options */
  async getAuth(model, options) {
    getAuthCalls += 1;
    assert.equal(model.provider, "openai-codex");
    assert.ok(options.signal instanceof AbortSignal);
    return { auth: { apiKey: credentialSentinel }, source: "synthetic fixture" };
  }
}

/** @type {typeof globalThis.fetch} */
globalThis.fetch = async (input, init) => {
  fetchCalls += 1;
  assert.equal(input, "https://chatgpt.com/backend-api/wham/usage");
  const headers = new Headers(init?.headers);
  const authorizationMatches = headers.get("authorization") === `Bearer ${credentialSentinel}`;
  const accountMatches = headers.get("chatgpt-account-id") === accountID;
  assert.equal(authorizationMatches, true);
  assert.equal(accountMatches, true);
  assert.equal(init?.redirect, "error");
  assert.ok(init?.signal instanceof AbortSignal);

  const waitFile = process.env.STEWARD_QUOTA_FIXTURE_WAIT_FILE;
  if (waitFile !== undefined) {
    appendFileSync(marker, `${JSON.stringify({ phase: "started", safe: true })}\n`);
    await waitForFile(waitFile);
  }
  appendFileSync(marker, `${JSON.stringify({
    phase: "complete",
    getAuthCalls,
    fetchCalls,
    authorizationMatches,
    accountMatches,
  })}\n`);
  return new Response(JSON.stringify({
    rate_limit: {
      primary_window: {
        limit_window_seconds: 604800,
        used_percent: 80,
        reset_after_seconds: 604800,
      },
      secondary_window: {
        limit_window_seconds: 18000,
        used_percent: 25,
        reset_after_seconds: 3600,
      },
    },
  }), { status: 200, headers: { "content-type": "application/json" } });
};
