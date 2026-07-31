defmodule Demo.Format do
  @moduledoc "Uppercase helper used by Demo.Greeter.greet."

  def apply(s) when is_binary(s), do: String.upcase(s)

  def apply!(s) when is_binary(s) do
    case apply(s) do
      "" -> raise ArgumentError, "empty"
      out -> out
    end
  end
end
