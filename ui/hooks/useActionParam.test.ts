import { describe, expect, it } from "vitest";

import { withActionParam } from "./useActionParam";

describe("withActionParam", () => {
	it("adds the action as the first query param", () => {
		expect(withActionParam("/workspace/governance/virtual-keys", "create")).toBe("/workspace/governance/virtual-keys?action=create");
	});

	it("appends to an href that already has a query", () => {
		expect(withActionParam("/workspace/config/caching?tab=semantic", "create")).toBe(
			"/workspace/config/caching?tab=semantic&action=create",
		);
	});

	it("encodes the action", () => {
		expect(withActionParam("/x", "a b&c")).toBe("/x?action=a%20b%26c");
	});
});