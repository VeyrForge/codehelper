import SwiftUI

/// HomeView → DetailView (NavigationLink) + GreetingView.
struct HomeView: View {
    var body: some View {
        NavigationStack {
            VStack {
                GreetingView()
                NavigationLink("Detail") {
                    DetailView()
                }
                NavigationLink(destination: DetailScreen()) {
                    Text("Screen")
                }
            }
        }
    }
}
