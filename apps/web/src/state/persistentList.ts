// 只读的有序快照；32 分支树按路径复制，追加和替换不复制整个历史。
// 不持有祖先快照，也不保存外部资源；协议序列化仍输出普通数组。

const WIDTH = 32;
type Node<T> = readonly (Node<T> | T)[];

/** 普通只读数组与持久化列表共有的最小读取接口。 */
export interface ReadonlyList<T> extends Iterable<T> {
  readonly length: number;
  at(index: number): T | undefined;
  slice(start?: number, end?: number): T[];
}

function boundary(index: number, length: number): number {
  const integer = Math.trunc(index) || 0;
  return integer < 0 ? Math.max(0, length + integer) : Math.min(integer, length);
}

function replaceNode<T>(node: Node<T>, height: number, index: number, value: T): Node<T> {
  const copy = node.slice();
  if (height === 0) copy[index % WIDTH] = value;
  else {
    const span = WIDTH ** height;
    const slot = Math.floor(index / span) % WIDTH;
    copy[slot] = replaceNode((node[slot] as Node<T> | undefined) ?? [], height - 1, index, value);
  }
  return copy;
}

function* walk<T>(node: Node<T>, height: number): Generator<T> {
  if (height === 0) yield* node as readonly T[];
  else for (const child of node) yield* walk(child as Node<T>, height - 1);
}

/** 分块持久化列表；每次写入至多复制根路径上的有界块，旧快照永不变更。 */
export class PersistentList<T> implements ReadonlyList<T> {
  readonly #root: Node<T>;
  readonly #height: number;
  private constructor(root: Node<T>, height: number, readonly length: number) {
    this.#root = root;
    this.#height = height;
  }

  static from<T>(values: Iterable<T>): PersistentList<T> {
    if (values instanceof PersistentList) return values;
    const items = Array.from(values);
    let level: Node<T>[] = [];
    for (let index = 0; index < items.length; index += WIDTH) level.push(items.slice(index, index + WIDTH));
    if (!level.length) level = [[]];
    let height = 0;
    while (level.length > 1) {
      const parent: Node<T>[] = [];
      for (let index = 0; index < level.length; index += WIDTH) parent.push(level.slice(index, index + WIDTH));
      level = parent;
      height++;
    }
    return new PersistentList(level[0]!, height, items.length);
  }

  at(index: number): T | undefined {
    const integer = Math.trunc(index) || 0;
    const position = integer < 0 ? this.length + integer : integer;
    if (position < 0 || position >= this.length) return undefined;
    let node = this.#root;
    for (let height = this.#height; height > 0; height--) node = node[Math.floor(position / WIDTH ** height) % WIDTH] as Node<T>;
    return node[position % WIDTH] as T;
  }

  append(value: T): PersistentList<T> {
    if (this.length === WIDTH ** (this.#height + 1)) {
      const root: Node<T> = [this.#root, replaceNode([], this.#height, this.length, value)];
      return new PersistentList(root, this.#height + 1, this.length + 1);
    }
    return new PersistentList(replaceNode(this.#root, this.#height, this.length, value), this.#height, this.length + 1);
  }

  set(index: number, value: T): PersistentList<T> {
    if (!Number.isInteger(index) || index < 0 || index >= this.length) throw new RangeError("PersistentList index out of bounds");
    return new PersistentList(replaceNode(this.#root, this.#height, index, value), this.#height, this.length);
  }

  slice(start = 0, end = this.length): T[] {
    const from = boundary(start, this.length), to = boundary(end, this.length);
    const items: T[] = [];
    for (let index = from; index < to; index++) items.push(this.at(index)!);
    return items;
  }

  [Symbol.iterator](): Iterator<T> { return walk(this.#root, this.#height); }
  toJSON(): T[] { return Array.from(this); }
}

/** 小列表沿用原生数组；超过四个叶块后，仅复制有界树节点。 */
export function writeList<T>(list: ReadonlyList<T>, index: number, value: T): ReadonlyList<T> {
  if (!Number.isInteger(index) || index < 0 || index > list.length) throw new RangeError("PersistentList index out of bounds");
  if (list instanceof PersistentList || list.length >= WIDTH * 4) {
    const persistent = PersistentList.from(list);
    return index === list.length ? persistent.append(value) : persistent.set(index, value);
  }
  const copy = list.slice();
  copy[index] = value;
  return copy;
}
