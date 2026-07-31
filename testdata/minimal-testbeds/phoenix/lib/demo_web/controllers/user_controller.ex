defmodule DemoWeb.UserController do
  use DemoWeb, :controller

  def create(conn, %{"name" => name}) do
    json(conn, %{ok: true, name: name})
  end
end
