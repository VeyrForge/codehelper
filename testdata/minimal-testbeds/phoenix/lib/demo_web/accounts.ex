defmodule DemoWeb.Accounts do
  def greet(name) when is_binary(name), do: "hello " <> name
end
