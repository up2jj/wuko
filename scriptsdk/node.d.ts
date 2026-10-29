/** Wuko injects this API into Node processes launched by shell.with.sdk: node. */
export type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };
export type JSONObject = { [key: string]: JSONValue };

export interface WukoSDK {
  readonly args: Readonly<JSONObject>;
  readonly vars: Readonly<JSONObject>;
  readonly inputs: Readonly<JSONObject>;
  readonly steps: Readonly<JSONObject>;
  readonly dependencies: Readonly<JSONObject>;
  readonly env: Readonly<Record<string, string>>;
  readonly providers: Readonly<JSONObject>;
  readonly bindings: Readonly<JSONObject>;
  readonly workflow: Readonly<{ name: string; dir: string; timezone: string }>;
  readonly run: Readonly<{ dir: string; environment_loaders: readonly string[]; os: string; arch: string; interactive: boolean }>;
  readonly attempt: Readonly<{ number: number; max_attempts: number; operation_id: string }>;
  readonly helpers: Readonly<Record<string, (...args: JSONValue[]) => Promise<JSONValue>>>;
  output(name: string, value: JSONValue): Promise<null>;
  setVar(name: string, value: JSONValue): Promise<null>;
  secret(reference: string): Promise<string>;
  render(template: string, extra?: JSONObject): Promise<string>;
  step(type: string, config?: JSONObject): Promise<JSONObject>;
  runWorkflow(name: string, options?: { target?: string; vars?: JSONObject }): Promise<JSONObject>;
}

declare global {
  const wuko: WukoSDK;
}
