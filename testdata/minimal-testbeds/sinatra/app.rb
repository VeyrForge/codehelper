require_relative "lib/sinatra/base"

class App < Sinatra::Base
  get "/" do
    "hello"
  end
end
