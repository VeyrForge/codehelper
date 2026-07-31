defmodule Demo.Greeter do
  alias Demo.Format
  import Demo.Helpers
  use GenServer
  @behaviour GenServer

  @doc "Public greet entry — pipe normalize then Format.apply."
  def greet(name) do
    name
    |> normalize()
    |> Format.apply()
  end

  def start_link(opts \\ []) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  @impl true
  def init(opts), do: {:ok, opts}

  defp normalize(name) when is_binary(name), do: String.trim(name)
end
