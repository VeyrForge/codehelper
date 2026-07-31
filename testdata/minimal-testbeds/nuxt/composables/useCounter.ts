export function useCounter() {
  const n = useState("count", () => 0);
  function increment() {
    n.value++;
  }
  return { n, increment };
}
