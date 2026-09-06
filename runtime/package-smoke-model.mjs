import assert from "node:assert/strict";
import { appendFileSync } from "node:fs";
import { registerHooks } from "node:module";
import process from "node:process";

const marker = process.env.STEWARD_PACKAGE_SMOKE_MARKER;
if (marker === undefined) throw new Error("package smoke model marker is required");
const markerPath = marker;
const self = import.meta.url;

/** @typedef {{ signal: AbortSignal, allowModelNetwork: false }} CreateOptions */
/** @typedef {{ provider: string, id: string }} Model */
/** @typedef {{ systemPrompt: string, messages: unknown[] }} CompletionContext */
/** @typedef {{ signal: AbortSignal, maxTokens: 256, maxRetries: 0, timeoutMs: 15000, reasoning: "low" }} CompletionOptions */

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@earendil-works/pi-coding-agent") {
      return { url: self, shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});

export class ModelRuntime {
  /** @param {CreateOptions} options */
  static async create(options) {
    assert.deepEqual(options, { signal: options.signal, allowModelNetwork: false });
    return new ModelRuntime();
  }

  /** @param {string} provider @param {string} id @returns {Model} */
  getModel(provider, id) {
    assert.equal(provider, "openai-codex");
    assert.equal(id, "gpt-5.6-luna");
    return { provider, id };
  }

  /** @param {Model} model @param {CompletionContext} context @param {CompletionOptions} options */
  async completeSimple(model, context, options) {
    assert.deepEqual(model, { provider: "openai-codex", id: "gpt-5.6-luna" });
    assert.equal(typeof context.systemPrompt, "string");
    assert.equal(context.messages.length, 1);
    assert.deepEqual(options, {
      signal: options.signal,
      maxTokens: 256,
      maxRetries: 0,
      timeoutMs: 15000,
      reasoning: "low",
    });
    appendFileSync(markerPath, `${JSON.stringify({ options, calls: 1 })}\n`);
    return {
      stopReason: "stop",
      content: [{ type: "text", text: '{"body":"Synthetic package outcome","label":"Synthetic Package Label"}' }],
    };
  }
}
