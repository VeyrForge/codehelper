import { greet } from "../lib/greet";

export default function Page() {
  return <h1>{greet("next")}</h1>;
}
