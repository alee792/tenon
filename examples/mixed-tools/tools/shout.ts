import { z } from "zod";

// A tool is a default export: a description, strict Zod input/output schemas,
// and an execute function. The host validates arguments against inputSchema
// before execute runs and validates the result against outputSchema before it
// leaves, so execute receives already-parsed input.
export default {
  description: "Uppercase a string and append an exclamation mark.",
  inputSchema: z.strictObject({ text: z.string() }),
  outputSchema: z.strictObject({ shouted: z.string() }),
  execute(input: { text: string }) {
    return { shouted: input.text.toUpperCase() + "!" };
  },
};
