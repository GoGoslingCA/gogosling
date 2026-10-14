import { describe, expect, it } from "vitest";
import { PersistentList, writeList, type ReadonlyList } from "./persistentList";

describe("有界分块的不可变有序列表", () => {
  it("跨叶块及多级根扩展仍保持完整顺序，旧快照和分叉不变", () => {
    let current: ReadonlyList<number> = [];
    const snapshots = new Map<number, ReadonlyList<number>>();
    const boundaries = new Set([0, 31, 32, 33, 63, 64, 65, 1023, 1024, 1025, 32767, 32768, 32769]);
    for (let index = 0; index <= 32769; index++) {
      if (boundaries.has(index)) snapshots.set(index, current);
      current = writeList(current, current.length, index);
    }
    expect(Array.from(current)).toEqual(Array.from({ length: 32770 }, (_, index) => index));
    for (const [length, snapshot] of snapshots) {
      expect(snapshot.length).toBe(length);
      expect(Array.from(snapshot)).toEqual(Array.from({ length }, (_, index) => index));
      if (!length) continue;
      const branch = writeList(snapshot, Math.floor(length / 2), -1);
      expect(branch.at(Math.floor(length / 2))).toBe(-1);
      expect(snapshot.at(Math.floor(length / 2))).toBe(Math.floor(length / 2));
      expect(branch.at(-1)).toBe(length === 1 ? -1 : length - 1);
    }
  });

  it("从旧数组转换不持有可变数组块，嵌套值和 undefined 保留原引用", () => {
    const object = { value: 1 }, nested = ["nested"];
    const input = [object, nested, undefined, ...Array.from({ length: 70 }, (_, index) => index)];
    const list = PersistentList.from<unknown>(input);
    input[0] = 999; input.push(999);
    expect(list.length).toBe(73);
    expect(list.at(0)).toBe(object);
    expect(list.at(1)).toBe(nested);
    expect(list.at(2)).toBeUndefined();
    const updated = list.set(70, "updated");
    expect(list.at(70)).toBe(67);
    expect(updated.at(70)).toBe("updated");
    expect(updated.at(0)).toBe(object);
    expect(JSON.parse(JSON.stringify(list))).toEqual([object, nested, null, ...Array.from({ length: 70 }, (_, index) => index)]);
  });

  it("分页边界、负索引和无穷边界与数组读取一致", () => {
    const input = Array.from({ length: 1200 }, (_, index) => index);
    const list = PersistentList.from(input);
    for (const index of [-Infinity, -1201, -1200, -2.2, -1, 0, NaN, 31, 32, 1024, 1199, 1200, Infinity]) expect(list.at(index)).toBe(input.at(index));
    for (const start of [-Infinity, -1300, -45, 0, NaN, 31, 32, 1020, 1199, Infinity]) {
      for (const end of [-Infinity, -10, 0, NaN, 40, 1050, 1200, Infinity]) expect(list.slice(start, end)).toEqual(input.slice(start, end));
    }
    expect(() => list.set(-1, 0)).toThrow(RangeError);
    expect(() => list.set(1200, 0)).toThrow(RangeError);
    expect(() => writeList([1], 2, 0)).toThrow(RangeError);
    expect(() => writeList(list, -1, 0)).toThrow(RangeError);
  });

  it("随机旧分支追加和替换与不可变数组参考实现一致", () => {
    const versions: { list: ReadonlyList<number>; expected: number[] }[] = [{ list: PersistentList.from(Array.from({ length: 2050 }, (_, index) => index)), expected: Array.from({ length: 2050 }, (_, index) => index) }];
    let seed = 113;
    const random = () => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed; };
    for (let index = 0; index < 700; index++) {
      const previous = versions[index % 7 === 0 ? random() % versions.length : versions.length - 1]!;
      const position = index % 3 === 0 ? previous.list.length : random() % previous.list.length;
      const expected = previous.expected.slice(); expected[position] = -index;
      const list = writeList(previous.list, position, -index);
      expect(list.slice()).toEqual(expected);
      expect(Array.from(previous.list)).toEqual(previous.expected);
      versions.push({ list, expected });
    }
  });
});
