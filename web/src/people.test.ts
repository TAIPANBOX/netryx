import { test } from "node:test";
import assert from "node:assert/strict";
import { personId, ownerOf } from "./people";

test("a new person gets an id from the name, or a free one", () => {
  assert.equal(personId("Sam", []), "p_sam");
  assert.equal(personId("Sam O'Neil", []), "p_sam_o_neil");
  assert.equal(personId("Sam", ["p_sam"]), "p_sam_2");
  assert.equal(personId("Сашко", []), "p_1"); // a name in another script keeps its name, the id is a number
  assert.equal(personId("Сашко", ["p_1", "p_2"]), "p_3");
  assert.equal(personId("A very long name that goes on and on and on", []).length <= 32, true);
  assert.match(personId("A very long name that goes on and on and on", []), /^p_[a-z0-9_]+$/);
});

test("a device names its person, if it has one", () => {
  const people = [{ id: "sam", name: "Sam", macs: ["aa:bb:cc:dd:ee:01"] }];
  assert.deepEqual(ownerOf(people, "aa:bb:cc:dd:ee:01"), { id: "sam", name: "Sam" });
  assert.equal(ownerOf(people, "aa:bb:cc:dd:ee:02"), undefined);
  assert.equal(ownerOf(undefined, "aa:bb:cc:dd:ee:01"), undefined);
});
