export type ApiMapping = { operationId: string; inputs: Record<string, string>; exclusion?: string };
export type CommandMetadata = {
  provider?: string; dependsOn?: string; suggestions?: string[]; appendSpace?: boolean;
  prompt?: boolean; defaultCommand?: string; passthrough?: boolean;
  capability?: "cli" | "files" | "interactive"; utility?: boolean; api?: ApiMapping[];
};
export type ArgumentDefinition = {
  name: string; description?: string; required?: boolean; hidden?: boolean;
  arity?: { minimum: number; maximum?: number }; acceptedValues?: string[];
  metadata?: { name: string; value: CommandMetadata }[];
};
export type OptionDefinition = ArgumentDefinition & { aliases?: string[]; arguments?: ArgumentDefinition[]; recursive?: boolean };
export type CommandDefinition = ArgumentDefinition & { aliases?: string[]; options?: OptionDefinition[]; arguments?: ArgumentDefinition[]; commands?: CommandDefinition[] };
export type OpenCLI = { opencli: string; info: { title: string; version: string }; command: CommandDefinition };
export const metadata = (item: ArgumentDefinition): CommandMetadata => item.metadata?.find(entry => entry.name === "floatlab")?.value ?? {};
export const names = (item: CommandDefinition | OptionDefinition) => [item.name, ...(item.aliases ?? [])];
