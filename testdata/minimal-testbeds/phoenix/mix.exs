defmodule DemoWeb.MixProject do
  use Mix.Project

  def project do
    [
      app: :demo_web,
      version: "0.1.0",
      elixir: "~> 1.15",
      start_permanent: Mix.env() == :prod,
      deps: [
        {:phoenix, "~> 1.7.0"}
      ]
    ]
  end

  def application do
    [extra_applications: [:logger]]
  end
end
