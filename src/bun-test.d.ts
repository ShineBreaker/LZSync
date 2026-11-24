declare module "bun:test" {
  interface Matchers<T> {
    toBe(expected: T): void;
    toBeUndefined(): void;
    not: Matchers<T>;
  }

  export function expect<T>(value: T): Matchers<T>;
  export function test(name: string, callback: () => void | Promise<void>): void;
}
