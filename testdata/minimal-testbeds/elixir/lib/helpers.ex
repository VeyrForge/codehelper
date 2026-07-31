defmodule Demo.Helpers do
  @moduledoc "Imported helpers for the Elixir lite bed."

  def blank?(s) when is_binary(s), do: String.trim(s) == ""
  def blank?(_), do: true
end
