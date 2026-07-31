defmodule DemoWeb.PostController do
  use DemoWeb, :controller

  def index(conn, _params), do: json(conn, [])
  def show(conn, %{"id" => id}), do: json(conn, %{id: id})
end
