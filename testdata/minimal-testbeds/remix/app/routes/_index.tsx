import { greet, saveGreeting } from "../lib/greet";

/** Remix loader → greet (probe surface). */
export async function loader() {
  return { message: greet("remix") };
}

/** Remix action → saveGreeting. */
export async function action() {
  return { saved: saveGreeting("remix") };
}

export default function Index() {
  return <h1>remix</h1>;
}
