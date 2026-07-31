Rails.application.routes.draw do
  root to: "home#index"
  get "/users/:id", to: "users#show"
  get "/admin/users/:id", to: "admin/users#show"
  resources :posts do
    member do
      get :preview
    end
  end
  namespace :api do
    resources :accounts
  end
end
