import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// Explicit rather than relying on @testing-library/react's auto-cleanup
// detection, since this project doesn't enable vitest's `globals` option.
afterEach(cleanup);

// Recent Node versions ship their own experimental global `localStorage`
// (throws without --localstorage-file) that shadows jsdom's real
// window.localStorage in this test environment. Replace it with a plain
// in-memory Storage implementation so component code that reads the bare
// `localStorage` global behaves the same in tests as in an actual browser.
class MemoryStorage implements Storage {
  private store = new Map<string, string>();
  get length() {
    return this.store.size;
  }
  clear() {
    this.store.clear();
  }
  getItem(key: string) {
    return this.store.has(key) ? this.store.get(key)! : null;
  }
  key(index: number) {
    return [...this.store.keys()][index] ?? null;
  }
  removeItem(key: string) {
    this.store.delete(key);
  }
  setItem(key: string, value: string) {
    this.store.set(key, String(value));
  }
}
const memoryStorage = new MemoryStorage();
for (const target of [globalThis, window]) {
  Object.defineProperty(target, "localStorage", { value: memoryStorage, configurable: true });
}

// jsdom does no real layout: every element's offsetWidth/offsetHeight (what
// @tanstack/react-virtual actually reads to size its viewport — see
// measureElement in @tanstack/virtual-core) and getBoundingClientRect() are
// zero, and ResizeObserver doesn't exist. Without these stubs, react-virtual
// (used by TrafficTable) thinks its scroll container is zero-sized and
// renders an empty <tbody> regardless of how many entries were passed in.
for (const prop of ["offsetWidth", "clientWidth"] as const) {
  Object.defineProperty(HTMLElement.prototype, prop, { configurable: true, value: 800 });
}
for (const prop of ["offsetHeight", "clientHeight"] as const) {
  Object.defineProperty(HTMLElement.prototype, prop, { configurable: true, value: 600 });
}
Element.prototype.getBoundingClientRect = () =>
  ({ width: 800, height: 600, top: 0, left: 0, bottom: 600, right: 800, x: 0, y: 0, toJSON() {} }) as DOMRect;

class StubResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver = StubResizeObserver;
