defmodule DemoWeb.PageController do
  use DemoWeb, :controller

  alias DemoWeb.Accounts

  def index(conn, _params) do
    name = Accounts.greet("world")
    render(conn, :index, name: name)
  end
end
