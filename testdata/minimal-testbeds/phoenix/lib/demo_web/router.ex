defmodule DemoWeb.Router do
  use DemoWeb, :router

  pipeline :browser do
    plug :accepts
    plug :fetch_session
  end

  scope "/", DemoWeb do
    pipe_through :browser

    get "/", PageController, :index
    post "/users", UserController, :create
    live "/dashboard", DashboardLive, :index
    resources "/posts", PostController
  end
end
